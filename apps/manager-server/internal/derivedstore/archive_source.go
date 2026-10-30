package derivedstore

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type archivedEventRef struct {
	id        int64
	hash      string
	timestamp int64
	fileName  string
}

type archiveEnvelope struct {
	SchemaVersion int    `json:"_cpamp_archive_schema_version"`
	EventID       int64  `json:"_cpamp_archive_event_id"`
	EventHash     string `json:"event_hash"`
	TimestampMS   int64  `json:"timestamp_ms"`
}

// ArchivedEventsAfter reads only deleted raw events whose archive references
// are already published. The SQL query is bounded by event id and LIMIT; file
// decoding is grouped by segment so a visible-page read never scans every
// archive file in the directory.
func (s *sqlSource) ArchivedEventsAfter(ctx context.Context, afterID, targetID int64, limit int) ([]EventRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("derived source is unavailable")
	}
	if limit <= 0 || targetID <= afterID {
		return []EventRecord{}, nil
	}
	rows, err := s.db.QueryContext(ctx, `select
		r.raw_event_id, r.event_hash, r.timestamp_ms, s.file_name
	from usage_archive_event_refs r
	join usage_archive_segments s
		on s.run_id = r.run_id and s.sequence = r.segment_sequence
	where r.raw_deleted_at_ms is not null
		and r.raw_event_id > ? and r.raw_event_id <= ?
		and s.status in ('published', 'verified')
	order by r.raw_event_id asc
	limit ?`, afterID, targetID, limit)
	if err != nil {
		return nil, fmt.Errorf("read bounded archive references: %w", err)
	}
	defer rows.Close()
	bySegment := make(map[string]map[int64]archivedEventRef)
	for rows.Next() {
		var ref archivedEventRef
		if err := rows.Scan(&ref.id, &ref.hash, &ref.timestamp, &ref.fileName); err != nil {
			return nil, fmt.Errorf("scan archive reference: %w", err)
		}
		ref.fileName = filepath.ToSlash(strings.TrimSpace(ref.fileName))
		if ref.id <= 0 || ref.timestamp <= 0 || ref.hash == "" || ref.fileName == "" {
			return nil, errors.New("archive reference contains incomplete identity")
		}
		segment := bySegment[ref.fileName]
		if segment == nil {
			segment = make(map[int64]archivedEventRef)
			bySegment[ref.fileName] = segment
		}
		segment[ref.id] = ref
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate archive references: %w", err)
	}
	if len(bySegment) == 0 {
		return []EventRecord{}, nil
	}
	if strings.TrimSpace(s.archiveDir) == "" {
		return nil, errors.New("archived usage source directory is not configured")
	}

	result := make([]EventRecord, 0, minInt(limit, 256))
	for fileName, refs := range bySegment {
		events, err := readArchivedSegment(ctx, s.archiveDir, fileName, refs)
		if err != nil {
			return nil, err
		}
		result = append(result, events...)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func readArchivedSegment(ctx context.Context, archiveDir, fileName string, refs map[int64]archivedEventRef) ([]EventRecord, error) {
	pathName, err := resolveDerivedArchivePath(archiveDir, fileName)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(pathName)
	if err != nil {
		return nil, fmt.Errorf("open published archive segment: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat published archive segment: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("published archive segment is not a regular file")
	}
	zipper, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open published archive gzip: %w", err)
	}
	defer zipper.Close()
	scanner := bufio.NewScanner(zipper)
	scanner.Buffer(make([]byte, 64*1024), usage.MaxJSONLRecordBytes+3)
	result := make([]EventRecord, 0, len(refs))
	seen := make(map[int64]struct{}, len(refs))
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return nil, errors.New("published archive segment contains an empty record")
		}
		var envelope archiveEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return nil, fmt.Errorf("decode published archive envelope: %w", err)
		}
		ref, wanted := refs[envelope.EventID]
		if !wanted {
			continue
		}
		if envelope.SchemaVersion != usage.ArchiveSchemaVersion ||
			envelope.EventHash != ref.hash || envelope.TimestampMS != ref.timestamp {
			return nil, fmt.Errorf("published archive envelope mismatch for event %d", envelope.EventID)
		}
		parsed, err := usage.ParseImportPayload(line)
		if err != nil {
			return nil, fmt.Errorf("restore published archive event %d: %w", envelope.EventID, err)
		}
		if parsed.Format != usage.ImportFormatJSONL || parsed.Failed != 0 || parsed.Unsupported != 0 || len(parsed.Events) != 1 {
			return nil, fmt.Errorf("published archive event %d is not restorable", envelope.EventID)
		}
		event := parsed.Events[0]
		if !event.PreserveArchiveDerivedFields || event.EventHash != ref.hash || event.TimestampMS != ref.timestamp {
			return nil, fmt.Errorf("published archive event %d failed identity validation", envelope.EventID)
		}
		result = append(result, eventRecordFromArchiveEvent(ref.id, event))
		seen[ref.id] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read published archive segment: %w", err)
	}
	if len(seen) != len(refs) {
		return nil, fmt.Errorf("published archive segment is missing %d referenced event(s)", len(refs)-len(seen))
	}
	return result, nil
}

func resolveDerivedArchivePath(root, relativeName string) (string, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(relativeName) == "" ||
		path.IsAbs(relativeName) || filepath.IsAbs(relativeName) {
		return "", errors.New("invalid published archive path")
	}
	clean := path.Clean(filepath.ToSlash(relativeName))
	if clean != relativeName || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("invalid published archive path")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve archive root: %w", err)
	}
	candidate := filepath.Join(rootAbs, filepath.FromSlash(clean))
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("published archive path escapes archive root")
	}
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", fmt.Errorf("stat published archive path: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("published archive path is a symlink")
	}
	return candidate, nil
}

func eventRecordFromArchiveEvent(id int64, event usage.Event) EventRecord {
	return EventRecord{
		ID:                    id,
		EventHash:             event.EventHash,
		TimestampMS:           event.TimestampMS,
		Provider:              event.Provider,
		ExecutorType:          event.ExecutorType,
		Model:                 event.Model,
		RequestedModel:        event.RequestedModel,
		ResolvedModel:         event.ResolvedModel,
		AuthIndex:             event.AuthIndex,
		Source:                event.Source,
		SourceHash:            event.SourceHash,
		APIKeyHash:            event.APIKeyHash,
		AccountSnapshot:       event.AccountSnapshot,
		AuthLabelSnapshot:     event.AuthLabelSnapshot,
		AuthFileSnapshot:      event.AuthFileSnapshot,
		AuthProviderSnapshot:  event.AuthProviderSnapshot,
		AuthAccountIDSnapshot: event.AuthAccountIDSnapshot,
		AuthProjectIDSnapshot: event.AuthProjectIDSnapshot,
		ServiceTier:           event.ServiceTier,
		InputTokens:           event.InputTokens,
		OutputTokens:          event.OutputTokens,
		ReasoningTokens:       event.ReasoningTokens,
		CachedTokens:          event.CachedTokens,
		CacheTokens:           event.CacheTokens,
		CacheReadTokens:       event.CacheReadTokens,
		CacheCreationTokens:   event.CacheCreationTokens,
		NormalizedInputTokens: event.NormalizedTotalInputTokens,
		TotalTokens:           event.TotalTokens,
		LatencyMS:             event.LatencyMS,
		Failed:                event.Failed,
		FailStatusCode:        event.FailStatusCode,
		FailSummary:           event.FailSummary,
		HeaderQuotaRecoverAt:  event.HeaderQuotaRecoverAtMS,
		HeaderQuotaUsed:       event.HeaderQuotaUsedPercent,
		HeaderQuotaPlanType:   event.HeaderQuotaPlanType,
		HeaderErrorKind:       event.HeaderErrorKind,
		HeaderErrorCode:       event.HeaderErrorCode,
		HeaderTraceID:         event.HeaderTraceID,
	}
}
