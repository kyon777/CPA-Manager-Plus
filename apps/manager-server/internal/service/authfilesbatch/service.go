package authfilesbatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	MaxFiles     = 100
	MaxTotalSize = 32 * 1024 * 1024
	jobTTL       = 30 * time.Minute
)

type SetupResolver interface {
	ResolveSetup(context.Context) (store.Setup, bool, error)
}

type InventoryInvalidator interface {
	InvalidateAuthFileInventory(context.Context)
}

type UploadFile struct {
	Name string
	Data []byte
}

type ResultFile struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

type Status struct {
	ID        string       `json:"id"`
	State     string       `json:"state"`
	Total     int          `json:"total"`
	Completed int          `json:"completed"`
	Uploaded  int          `json:"uploaded"`
	Failed    int          `json:"failed"`
	Files     []ResultFile `json:"files,omitempty"`
	Finished  bool         `json:"finished"`
}

type uploadClient interface {
	Upload(context.Context, string, string, string, []byte) error
}

type Service struct {
	resolver    SetupResolver
	invalidator InventoryInvalidator
	client      uploadClient
	now         func() time.Time
	mu          sync.Mutex
	jobs        map[string]*job
}

type job struct {
	id         string
	createdAt  time.Time
	state      string
	total      int
	completed  int
	uploaded   int
	failed     int
	files      []ResultFile
	pending    []UploadFile
	finishedAt time.Time
}

func New(resolver SetupResolver, client ...uploadClient) *Service {
	return NewWithInventoryInvalidator(resolver, nil, client...)
}

func NewWithInventoryInvalidator(
	resolver SetupResolver,
	invalidator InventoryInvalidator,
	client ...uploadClient,
) *Service {
	configuredClient := uploadClient(cpaauthfiles.New(nil))
	if len(client) > 0 && client[0] != nil {
		configuredClient = client[0]
	}
	return &Service{
		resolver:    resolver,
		invalidator: invalidator,
		client:      configuredClient,
		now:         time.Now,
		jobs:        make(map[string]*job),
	}
}

func (s *Service) Start(ctx context.Context, files []UploadFile) (Status, error) {
	if s == nil || s.resolver == nil || s.client == nil {
		return Status{}, errors.New("auth-file batch upload is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return Status{}, err
	}
	if len(files) == 0 {
		return Status{}, errors.New("at least one auth file is required")
	}
	if len(files) > MaxFiles {
		return Status{}, fmt.Errorf("auth-file batch must contain at most %d files", MaxFiles)
	}
	totalSize := 0
	seen := make(map[string]struct{}, len(files))
	for index := range files {
		file := files[index]
		name := strings.TrimSpace(file.Name)
		if name == "" {
			return Status{}, errors.New("auth-file name is required")
		}
		if strings.ContainsAny(name, "\\/:") || name == "." || name == ".." {
			return Status{}, fmt.Errorf("invalid auth-file name %q", name)
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			return Status{}, fmt.Errorf("duplicate auth-file name %q", name)
		}
		seen[key] = struct{}{}
		totalSize += len(file.Data)
		if totalSize > MaxTotalSize {
			return Status{}, fmt.Errorf("auth-file batch exceeds %d bytes", MaxTotalSize)
		}
	}
	prepared := make([]UploadFile, 0, len(files))
	for index := range files {
		file := files[index]
		name := strings.TrimSpace(file.Name)
		// Ownership transfers to the in-memory job. The HTTP controller never
		// persists these bytes and returns immediately after this call.
		prepared = append(prepared, UploadFile{Name: name, Data: file.Data})
		files[index].Data = nil
	}

	id, err := randomID()
	if err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	s.cleanupLocked()
	item := &job{
		id:        id,
		createdAt: s.currentTime(),
		state:     "queued",
		total:     len(prepared),
		pending:   prepared,
	}
	s.jobs[id] = item
	status := snapshot(item)
	s.mu.Unlock()
	go s.run(id)
	return status, nil
}

func (s *Service) Get(id string) (Status, bool) {
	if s == nil {
		return Status{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	item, ok := s.jobs[strings.TrimSpace(id)]
	if !ok {
		return Status{}, false
	}
	return snapshot(item), true
}

func (s *Service) run(id string) {
	s.mu.Lock()
	item, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	item.state = "running"
	pending := item.pending
	item.pending = nil
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(len(pending)+1)*cpaauthfiles.DefaultTimeout)
	defer cancel()
	setup, configured, err := s.resolver.ResolveSetup(ctx)
	if err != nil || !configured {
		message := "CPA connection is unavailable"
		if err != nil {
			message = safeError(err)
		}
		for index := range pending {
			file := &pending[index]
			zeroBytes(file.Data)
			file.Data = nil
			s.record(id, file.Name, message, false)
		}
		return
	}
	for index := range pending {
		file := &pending[index]
		err := s.client.Upload(ctx, setup.CPAUpstreamURL, setup.ManagementKey, file.Name, file.Data)
		zeroBytes(file.Data)
		file.Data = nil
		if err != nil {
			s.record(id, file.Name, safeError(err), false)
			continue
		}
		s.record(id, file.Name, "", true)
	}
}

func (s *Service) record(id, name, failure string, uploaded bool) {
	s.mu.Lock()
	item, ok := s.jobs[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	item.completed++
	if uploaded {
		item.uploaded++
		item.files = append(item.files, ResultFile{Name: name})
	} else {
		item.failed++
		item.files = append(item.files, ResultFile{Name: name, Error: failure})
	}
	shouldInvalidate := false
	if item.completed >= item.total {
		if item.failed == 0 {
			item.state = "completed"
		} else if item.uploaded == 0 {
			item.state = "failed"
		} else {
			item.state = "partial"
		}
		item.finishedAt = s.currentTime()
		shouldInvalidate = item.uploaded > 0
	}
	s.mu.Unlock()
	if shouldInvalidate && s.invalidator != nil {
		s.invalidator.InvalidateAuthFileInventory(context.Background())
	}
}

func (s *Service) cleanupLocked() {
	if s == nil {
		return
	}
	cutoff := s.currentTime().Add(-jobTTL)
	for id, item := range s.jobs {
		if !item.finishedAt.IsZero() && item.finishedAt.Before(cutoff) {
			for index := range item.pending {
				item.pending[index].Data = nil
			}
			delete(s.jobs, id)
		}
	}
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (s *Service) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func snapshot(item *job) Status {
	if item == nil {
		return Status{}
	}
	files := append([]ResultFile(nil), item.files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return Status{
		ID:        item.id,
		State:     item.state,
		Total:     item.total,
		Completed: item.completed,
		Uploaded:  item.uploaded,
		Failed:    item.failed,
		Files:     files,
		Finished:  item.completed >= item.total,
	}
}

func randomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate batch upload id: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

func safeError(err error) string {
	if err == nil {
		return "upload failed"
	}
	text := strings.TrimSpace(err.Error())
	if len(text) > 240 {
		return text[:240]
	}
	if text == "" {
		return "upload failed"
	}
	return text
}
