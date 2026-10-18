package proxyreplacement

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestPlanPoolPrefersUnusedThenRotatesAllNormalCandidates(t *testing.T) {
	unused := []proxyCandidate{{URL: "http://p1", Fingerprint: "p1"}}
	all := []proxyCandidate{
		{URL: "http://p1", Fingerprint: "p1"},
		{URL: "http://p2", Fingerprint: "p2", InUse: true},
	}
	plan := allocatePool(1, unused, all, 5)
	got := make([]string, 0, len(plan))
	for _, assignment := range plan {
		got = append(got, assignment.Candidate.URL)
	}
	want := []string{"http://p1", "http://p2", "http://p1", "http://p2", "http://p1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("planned proxy URLs = %#v, want %#v", got, want)
	}
	if plan[0].Source != "unused" || plan[1].Source != "fallback" {
		t.Fatalf("allocation sources = %#v", plan)
	}
}

func TestWorkerResumesWithoutOverwritingChangedProxy(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "alice.json", "42", "alice@example.com", "http://old", false),
	}}
	service := newWorkerService(tasks, authFiles, nil)
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source:          model.ProxyReplacementSourceBatchUniform,
		Mode:            model.ProxyReplacementModeUniform,
		UniformProxyURL: "http://new",
		Targets: []model.ProxyReplacementTarget{{
			ClientKey: "alice", FileName: "alice.json", AuthIndex: "42", AccountEmail: "alice@example.com", Provider: "codex", RuntimeID: "runtime-1",
		}},
	})
	if err != nil {
		t.Fatalf("create preview: %v", err)
	}
	authFiles.setProxy("runtime-1", "http://someone-else")
	if _, err := service.Confirm(context.Background(), preview.Task.ID); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := service.RunPending(context.Background()); err != nil {
		t.Fatalf("run pending: %v", err)
	}
	page, err := tasks.GetTask(context.Background(), preview.Task.ID, 1, 50)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got := page.Items[0].Status; got != model.ProxyReplacementItemSkippedStale {
		t.Fatalf("item status = %q, want stale skip", got)
	}
	if authFiles.patchCalls != 0 || authFiles.proxyFor("runtime-1") != "http://someone-else" {
		t.Fatalf("stale item was overwritten: patchCalls=%d proxy=%q", authFiles.patchCalls, authFiles.proxyFor("runtime-1"))
	}
}

func TestManualBatchDoesNotProbeAndWritesOnlyProxyURL(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "a@example.com", "http://old", false),
	}}
	probes := &recordingRechecker{}
	service := newWorkerService(tasks, authFiles, probes)
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source:          model.ProxyReplacementSourceBatchUniform,
		Mode:            model.ProxyReplacementModeUniform,
		UniformProxyURL: "http://new/",
		Targets: []model.ProxyReplacementTarget{{
			ClientKey: "one", FileName: "one.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Confirm(context.Background(), preview.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if probes.calls != 0 {
		t.Fatalf("manual batch unexpectedly probed %d time(s)", probes.calls)
	}
	if !reflect.DeepEqual(authFiles.lastPatch, map[string]string{"name": "runtime-1", "proxy_url": "http://new"}) {
		t.Fatalf("direct patch = %#v", authFiles.lastPatch)
	}
}

func TestExpiredDraftCannotBeConfirmed(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "a@example.com", "http://old", false),
	}}
	service := newWorkerService(tasks, authFiles, nil)
	now := time.Date(2026, 10, 18, 4, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source:          model.ProxyReplacementSourceBatchUniform,
		Mode:            model.ProxyReplacementModeUniform,
		UniformProxyURL: "http://new",
		Targets: []model.ProxyReplacementTarget{{
			ClientKey: "one", FileName: "one.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(previewTTL + time.Second) }
	if _, err := service.Confirm(context.Background(), preview.Task.ID); !errors.Is(err, ErrTaskExpired) {
		t.Fatalf("confirm expired draft error = %v, want ErrTaskExpired", err)
	}
}

func TestCreatePreviewExcludesSuspectAndInspectionOldProxy(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{
		files: []cpaauthfiles.File{
			proxyTestFile("runtime-1", "one.json", "1", "a@example.com", "http://old", false),
			proxyTestFile("runtime-2", "two.json", "2", "b@example.com", "http://p2", false),
		},
		expectedBaseURL:       "http://cpa.internal:8317",
		expectedManagementKey: "manager-key",
	}
	service := NewWithOptions(Options{
		Tasks:         tasks,
		SetupResolver: staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		Settings: staticSettings{settings: store.ProxyFilterSettings{URLs: []string{
			"http://p1/", "http://p2", "http://suspect", "http://p1",
		}}},
		PoolState: staticPoolState{states: []model.ProxyPoolState{{
			Fingerprint: model.ProxyFingerprint("http://suspect"),
			Status:      model.ProxyPoolStatusSuspect,
		}}},
		AuthFiles: authFiles,
	})
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
		Targets: []model.ProxyReplacementTarget{{
			ClientKey: "run:1", FileName: "one.json", AuthIndex: "1", AccountEmail: "a@example.com", Provider: "codex", RuntimeID: "runtime-1",
		}},
	})
	if err != nil {
		t.Fatalf("create preview: %v", err)
	}
	if preview.Task.Status != model.ProxyReplacementTaskDraft || !preview.Task.CanConfirm {
		t.Fatalf("preview task = %#v", preview.Task)
	}
	if preview.Task.SuspectExcludedCount != 1 || preview.Task.UnusedNormalCount != 1 || preview.Task.FallbackNormalCount != 1 {
		t.Fatalf("pool counts = %#v", preview.Task)
	}
	if len(preview.Items) != 1 || preview.Items[0].NewProxyURL != "http://p1" || preview.Items[0].NewProxyURL == "http://old" {
		t.Fatalf("preview items = %#v", preview.Items)
	}
	if len(tasks.items) != 1 || tasks.items[0].OldProxyURL != "http://old" {
		t.Fatalf("frozen items = %#v", tasks.items)
	}
}

type previewTaskStore struct {
	task   model.ProxyReplacementTask
	items  []model.ProxyReplacementTaskItem
	nextID int64
}

func (s *previewTaskStore) CreateDraft(_ context.Context, task model.ProxyReplacementTask, items []model.ProxyReplacementTaskItem) (model.ProxyReplacementTask, error) {
	s.nextID++
	task.ID = s.nextID
	task.TotalCount = len(items)
	if task.Status == "" {
		task.Status = model.ProxyReplacementTaskDraft
	}
	s.task = task
	s.items = append([]model.ProxyReplacementTaskItem(nil), items...)
	for index := range s.items {
		s.items[index].ID = int64(index + 1)
		s.items[index].TaskID = task.ID
	}
	return task, nil
}

func (s *previewTaskStore) GetTask(_ context.Context, taskID int64, page, pageSize int) (model.ProxyReplacementTaskPage, error) {
	if taskID != s.task.ID {
		return model.ProxyReplacementTaskPage{}, errors.New("missing task")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	if start > len(s.items) {
		start = len(s.items)
	}
	end := start + pageSize
	if end > len(s.items) {
		end = len(s.items)
	}
	task := s.task
	task.CanConfirm = task.Status == model.ProxyReplacementTaskDraft && task.ExpiresAtMS > 0
	return model.ProxyReplacementTaskPage{Task: task, Items: append([]model.ProxyReplacementTaskItem(nil), s.items[start:end]...), Total: len(s.items), Page: page, PageSize: pageSize}, nil
}
func (s *previewTaskStore) ConfirmDraft(_ context.Context, taskID, nowMS int64) (model.ProxyReplacementTask, error) {
	if taskID != s.task.ID || s.task.Status != model.ProxyReplacementTaskDraft || (s.task.ExpiresAtMS > 0 && s.task.ExpiresAtMS <= nowMS) {
		return model.ProxyReplacementTask{}, errors.New("proxy replacement task cannot be confirmed")
	}
	s.task.Status = model.ProxyReplacementTaskQueued
	return s.task, nil
}
func (s *previewTaskStore) ClaimNextQueued(context.Context) (model.ProxyReplacementTask, bool, error) {
	if s.task.Status != model.ProxyReplacementTaskQueued {
		return model.ProxyReplacementTask{}, false, nil
	}
	s.task.Status = model.ProxyReplacementTaskRunning
	return s.task, true, nil
}
func (s *previewTaskStore) RequeueRunningOnStartup(context.Context) (int64, error) {
	if s.task.Status != model.ProxyReplacementTaskRunning {
		return 0, nil
	}
	s.task.Status = model.ProxyReplacementTaskQueued
	return 1, nil
}
func (s *previewTaskStore) MarkItem(_ context.Context, itemID int64, status model.ProxyReplacementItemStatus, outcome model.ProxyReplacementItemOutcome) error {
	for index := range s.items {
		if s.items[index].ID != itemID {
			continue
		}
		s.items[index].Status = status
		s.items[index].WriteErrorCode = outcome.WriteErrorCode
		s.items[index].WriteErrorMessage = outcome.WriteErrorMessage
		s.items[index].RecheckErrorCode = outcome.RecheckErrorCode
		s.items[index].RecheckErrorMessage = outcome.RecheckErrorMessage
		return nil
	}
	return errors.New("missing item")
}
func (s *previewTaskStore) CompleteTask(_ context.Context, taskID int64) (model.ProxyReplacementTask, error) {
	if taskID != s.task.ID {
		return model.ProxyReplacementTask{}, errors.New("missing task")
	}
	var updated, failed, skipped, recheckSuccess, recheckFailed int
	for _, item := range s.items {
		switch item.Status {
		case model.ProxyReplacementItemUpdated:
			updated++
		case model.ProxyReplacementItemRecheckSucceeded:
			updated++
			recheckSuccess++
		case model.ProxyReplacementItemRecheckFailed:
			updated++
			failed++
			recheckFailed++
		case model.ProxyReplacementItemWriteFailed:
			failed++
		case model.ProxyReplacementItemSkippedStale:
			skipped++
		}
	}
	s.task.UpdatedCount = updated
	s.task.FailedCount = failed
	s.task.SkippedCount = skipped
	s.task.RecheckSuccessCount = recheckSuccess
	s.task.RecheckFailedCount = recheckFailed
	s.task.Status = model.ProxyReplacementTaskCompleted
	if failed > 0 || skipped > 0 {
		s.task.Status = model.ProxyReplacementTaskPartial
	}
	return s.task, nil
}
func (s *previewTaskStore) ExpireDrafts(_ context.Context, nowMS int64) (int64, error) {
	if s.task.Status == model.ProxyReplacementTaskDraft && s.task.ExpiresAtMS > 0 && s.task.ExpiresAtMS <= nowMS {
		s.task.Status = model.ProxyReplacementTaskExpired
		return 1, nil
	}
	return 0, nil
}
func (s *previewTaskStore) ResetInFlightItems(context.Context) (int64, error) {
	var count int64
	for index := range s.items {
		if s.items[index].Status == model.ProxyReplacementItemWriting || s.items[index].Status == model.ProxyReplacementItemRecheckRunning {
			s.items[index].Status = model.ProxyReplacementItemPlanned
			count++
		}
	}
	return count, nil
}

type staticSettings struct {
	settings store.ProxyFilterSettings
}

func (s staticSettings) LoadProxyFilterSettings(context.Context) (store.ProxyFilterSettings, bool, error) {
	return s.settings, true, nil
}

type staticPoolState struct {
	states []model.ProxyPoolState
}

func (s staticPoolState) List(context.Context) ([]model.ProxyPoolState, error) { return s.states, nil }
func (s staticPoolState) MarkSuspect(context.Context, string, string) error    { return nil }

type staticSetupResolver struct {
	setup store.Setup
}

func (s staticSetupResolver) ResolveSetup(context.Context) (store.Setup, bool, error) {
	return s.setup, true, nil
}

type previewAuthFiles struct {
	files                 []cpaauthfiles.File
	expectedBaseURL       string
	expectedManagementKey string
	patchCalls            int
	lastPatch             map[string]string
}

func (s *previewAuthFiles) Fetch(_ context.Context, baseURL, managementKey string) ([]cpaauthfiles.File, error) {
	if s.expectedBaseURL != "" && (baseURL != s.expectedBaseURL || managementKey != s.expectedManagementKey) {
		return nil, errors.New("preview did not use the stored CPA connection")
	}
	return s.files, nil
}
func (s *previewAuthFiles) ResolveStatusMutationTarget(_ context.Context, _ string, _ string, selector, authIndex string) (cpaauthfiles.StatusMutationTarget, error) {
	for _, file := range s.files {
		if file.ID == selector && (authIndex == "" || file.AuthIndex == authIndex) {
			return cpaauthfiles.StatusMutationTarget{Selector: selector, Scope: cpaauthfiles.StatusMutationScopeCredential, File: file}, nil
		}
	}
	return cpaauthfiles.StatusMutationTarget{}, errors.New("missing patch target")
}
func (s *previewAuthFiles) PatchProxyURLTarget(_ context.Context, _ string, _ string, target cpaauthfiles.StatusMutationTarget, proxyURL string) error {
	for index := range s.files {
		if s.files[index].ID != target.File.ID {
			continue
		}
		s.files[index].Raw["proxy_url"] = proxyURL
		s.patchCalls++
		s.lastPatch = map[string]string{"name": target.File.ID, "proxy_url": proxyURL}
		return nil
	}
	return errors.New("missing patch target")
}
func (s *previewAuthFiles) Download(context.Context, string, string, string) ([]byte, error) {
	return nil, errors.New("not used")
}
func (s *previewAuthFiles) Upload(context.Context, string, string, string, []byte) error {
	return errors.New("not used")
}

func (s *previewAuthFiles) setProxy(runtimeID, proxyURL string) {
	for index := range s.files {
		if s.files[index].ID == runtimeID {
			s.files[index].Raw["proxy_url"] = proxyURL
		}
	}
}

func (s *previewAuthFiles) proxyFor(runtimeID string) string {
	for _, file := range s.files {
		if file.ID == runtimeID {
			return strings.TrimSpace(file.Raw["proxy_url"].(string))
		}
	}
	return ""
}

type recordingRechecker struct {
	calls int
}

func (r *recordingRechecker) RecheckProxyReplacement(_ context.Context, targets []model.ProxyReplacementTarget, _ int) []model.ProxyReplacementProbeResult {
	r.calls++
	results := make([]model.ProxyReplacementProbeResult, 0, len(targets))
	for _, target := range targets {
		results = append(results, model.ProxyReplacementProbeResult{Target: target, Succeeded: true})
	}
	return results
}

func newWorkerService(tasks *previewTaskStore, authFiles *previewAuthFiles, rechecker Rechecker) *Service {
	return NewWithOptions(Options{
		Tasks:         tasks,
		SetupResolver: staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		Settings:      staticSettings{settings: store.ProxyFilterSettings{}},
		PoolState:     staticPoolState{},
		AuthFiles:     authFiles,
		Rechecker:     rechecker,
	})
}

func proxyTestFile(id, name, authIndex, email, proxyURL string, disabled bool) cpaauthfiles.File {
	return cpaauthfiles.File{
		ID: id, Name: name, AuthIndex: authIndex, Provider: "codex", AccountSnapshot: email, Disabled: disabled,
		Raw: map[string]any{"proxy_url": proxyURL},
	}
}
