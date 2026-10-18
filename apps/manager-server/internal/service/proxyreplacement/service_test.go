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

func TestRecheckTransportVerifiedRequiresResponseEvidence(t *testing.T) {
	tests := []struct {
		name   string
		result model.ProxyReplacementProbeResult
		want   bool
	}{
		{name: "successful response", result: model.ProxyReplacementProbeResult{Succeeded: true}, want: true},
		{name: "http status response", result: model.ProxyReplacementProbeResult{ErrorCode: "http_status"}, want: true},
		{name: "http code response", result: model.ProxyReplacementProbeResult{ErrorCode: "http_401"}, want: true},
		{name: "missing credential", result: model.ProxyReplacementProbeResult{ErrorCode: "credential_not_found"}, want: false},
		{name: "suspected transport", result: model.ProxyReplacementProbeResult{ErrorCode: "http_status", ProxySuspect: true}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := recheckTransportVerified(tt.result); got != tt.want {
				t.Fatalf("recheckTransportVerified(%#v)=%v, want %v", tt.result, got, tt.want)
			}
		})
	}
}

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

func TestSafeTaskErrorRedactsProxyURLInsideStructuredError(t *testing.T) {
	message := safeTaskError(errors.New(`CPA rejected {"proxy_url":"http://user:password@proxy.example:8080/path"}`))
	if strings.Contains(message, "user:password@proxy.example") || strings.Contains(message, "http://") {
		t.Fatalf("proxy URL leaked in task error: %q", message)
	}
	if !strings.Contains(message, "[redacted-url]") {
		t.Fatalf("task error lost redaction marker: %q", message)
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

func TestWorkerUsesTargetedProxyLookupWithoutRefetchingFullInventory(t *testing.T) {
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
	if authFiles.fetchCalls != 1 {
		t.Fatalf("preview full inventory reads = %d, want 1", authFiles.fetchCalls)
	}
	authFiles.failFullFetchAfter = authFiles.fetchCalls
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
	if got := page.Items[0].Status; got != model.ProxyReplacementItemUpdated {
		t.Fatalf("item status = %q, want targeted update", got)
	}
	if got := authFiles.fetchCalls; got != 1 {
		t.Fatalf("worker unexpectedly refetched full inventory %d time(s)", got-1)
	}
	if got := authFiles.proxyResolveCalls; got != 1 {
		t.Fatalf("targeted proxy resolver calls = %d, want 1", got)
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

func TestInspectionRecheckMarksNewTransportFailedProxySuspectWithoutRollback(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "a@example.com", "http://old", false),
	}}
	pool := &recordingPoolState{}
	inspectionResults := &recordingInspectionResults{}
	probes := &recordingRechecker{results: []model.ProxyReplacementProbeResult{{
		Target:       model.ProxyReplacementTarget{ClientKey: "one"},
		Succeeded:    false,
		ProxySuspect: true,
		ErrorCode:    "request_error",
		ErrorMessage: "context deadline exceeded",
	}}}
	service := NewWithOptions(Options{
		Tasks: tasks,
		SetupResolver: staticSetupResolver{setup: store.Setup{
			CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key",
		}},
		Settings:          staticSettings{settings: store.ProxyFilterSettings{URLs: []string{"http://new"}}},
		PoolState:         pool,
		AuthFiles:         authFiles,
		Rechecker:         probes,
		InspectionResults: inspectionResults,
	})
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
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
	page, err := tasks.GetTask(context.Background(), preview.Task.ID, 1, 10)
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("task page=%#v err=%v", page, err)
	}
	if got := page.Items[0].Status; got != model.ProxyReplacementItemRecheckFailed {
		t.Fatalf("status=%q, want %q", got, model.ProxyReplacementItemRecheckFailed)
	}
	if got := authFiles.proxyFor("runtime-1"); got != "http://new" {
		t.Fatalf("proxy rollback or overwrite: %q", got)
	}
	if len(pool.calls) != 1 || pool.calls[0].proxyURL != "http://new" || pool.calls[0].reason != "transport_timeout" {
		t.Fatalf("pool suspect calls=%#v", pool.calls)
	}
	if len(inspectionResults.calls) != 0 {
		t.Fatalf("failed recheck must not clear inspection proxy state: %#v", inspectionResults.calls)
	}
}

func TestInspectionSuccessfulRecheckClearsOnlyThePersistentSourceResult(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "a@example.com", "http://old", false),
	}}
	inspectionResults := &recordingInspectionResults{}
	pool := &recordingPoolState{}
	service := NewWithOptions(Options{
		Tasks: tasks,
		SetupResolver: staticSetupResolver{setup: store.Setup{
			CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key",
		}},
		Settings:          staticSettings{settings: store.ProxyFilterSettings{URLs: []string{"http://new"}}},
		PoolState:         pool,
		AuthFiles:         authFiles,
		Rechecker:         &recordingRechecker{},
		InspectionResults: inspectionResults,
	})
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
		Targets: []model.ProxyReplacementTarget{{
			ClientKey:            "browser-row-7",
			FileName:             "one.json",
			AuthIndex:            "1",
			AccountEmail:         "a@example.com",
			Provider:             "codex",
			RuntimeID:            "runtime-1",
			InspectionRunID:      44,
			InspectionAccountKey: "one.json::1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a stale quarantine recorded after preview creation but before the
	// directed recheck completes.
	pool.states = []model.ProxyPoolState{{
		Fingerprint: model.ProxyFingerprint("http://new"),
		Status:      model.ProxyPoolStatusSuspect,
	}}
	if _, err := service.Confirm(context.Background(), preview.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(inspectionResults.calls) != 1 || len(inspectionResults.calls[0]) != 1 {
		t.Fatalf("inspection clear calls=%#v", inspectionResults.calls)
	}
	cleared := inspectionResults.calls[0][0]
	if cleared.InspectionRunID != 44 || cleared.InspectionAccountKey != "one.json::1" || cleared.ClientKey != "browser-row-7" {
		t.Fatalf("cleared target=%#v", cleared)
	}
	if got := pool.restored; !reflect.DeepEqual(got, []string{model.ProxyFingerprint("http://new")}) {
		t.Fatalf("restored pool fingerprints=%#v", got)
	}
}

func TestInspectionRecheckKeepsSharedProxySuspectWhenAnyTargetHasTransportFailure(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "one@example.com", "http://old-one", false),
		proxyTestFile("runtime-2", "two.json", "2", "two@example.com", "http://old-two", false),
	}}
	pool := &recordingPoolState{}
	probes := &recordingRechecker{results: []model.ProxyReplacementProbeResult{
		{Target: model.ProxyReplacementTarget{ClientKey: "one"}, Succeeded: true},
		{Target: model.ProxyReplacementTarget{ClientKey: "two"}, Succeeded: false, ProxySuspect: true, ErrorCode: "timeout", ErrorMessage: "context deadline exceeded"},
	}}
	service := NewWithOptions(Options{
		Tasks:         tasks,
		SetupResolver: staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		Settings:      staticSettings{settings: store.ProxyFilterSettings{URLs: []string{"http://shared"}}},
		PoolState:     pool,
		AuthFiles:     authFiles,
		Rechecker:     probes,
	})
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
		Targets: []model.ProxyReplacementTarget{
			{ClientKey: "one", FileName: "one.json", AuthIndex: "1", AccountEmail: "one@example.com", Provider: "codex", RuntimeID: "runtime-1"},
			{ClientKey: "two", FileName: "two.json", AuthIndex: "2", AccountEmail: "two@example.com", Provider: "codex", RuntimeID: "runtime-2"},
		},
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
	if got := pool.restored; len(got) != 0 {
		t.Fatalf("shared proxy restored despite transport failure: %#v", got)
	}
	if len(pool.calls) != 1 || pool.calls[0].proxyURL != "http://shared" {
		t.Fatalf("shared proxy suspect calls=%#v", pool.calls)
	}
}

func TestInspectionRecheckDoesNotRestoreSuspectPoolWithoutTransportEvidence(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "one@example.com", "http://old", false),
	}}
	pool := &recordingPoolState{}
	probes := &recordingRechecker{results: []model.ProxyReplacementProbeResult{{
		Target:       model.ProxyReplacementTarget{ClientKey: "one"},
		Succeeded:    false,
		ProxySuspect: false,
		ErrorCode:    "credential_not_found",
		ErrorMessage: "credential vanished before recheck",
	}}}
	service := NewWithOptions(Options{
		Tasks:         tasks,
		SetupResolver: staticSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://cpa.internal:8317", ManagementKey: "manager-key"}},
		Settings:      staticSettings{settings: store.ProxyFilterSettings{URLs: []string{"http://new"}}},
		PoolState:     pool,
		AuthFiles:     authFiles,
		Rechecker:     probes,
	})
	preview, err := service.CreatePreview(context.Background(), model.ProxyReplacementPreviewRequest{
		Source: model.ProxyReplacementSourceInspection,
		Mode:   model.ProxyReplacementModePool,
		Targets: []model.ProxyReplacementTarget{{
			ClientKey: "one", FileName: "one.json", AuthIndex: "1", AccountEmail: "one@example.com", Provider: "codex", RuntimeID: "runtime-1",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pool.states = []model.ProxyPoolState{{
		Fingerprint: model.ProxyFingerprint("http://new"),
		Status:      model.ProxyPoolStatusSuspect,
	}}
	if _, err := service.Confirm(context.Background(), preview.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RunPending(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := pool.restored; len(got) != 0 {
		t.Fatalf("pool restored without transport evidence: %#v", got)
	}
}

func TestRestoreAllSuspectProxyPoolEntriesRestoresOnlySuspectStates(t *testing.T) {
	pool := &recordingPoolState{states: []model.ProxyPoolState{
		{Fingerprint: "normal", Status: model.ProxyPoolStatusNormal},
		{Fingerprint: "suspect-a", Status: model.ProxyPoolStatusSuspect},
		{Fingerprint: "suspect-b", Status: model.ProxyPoolStatusSuspect},
	}}
	service := NewWithOptions(Options{PoolState: pool})
	restored, err := service.RestoreSuspectProxyPoolEntries(context.Background())
	if err != nil {
		t.Fatalf("restore suspect pool states: %v", err)
	}
	if restored != 2 {
		t.Fatalf("restored count=%d, want 2", restored)
	}
	if got := pool.restored; !reflect.DeepEqual(got, []string{"suspect-a", "suspect-b"}) {
		t.Fatalf("restored fingerprints=%#v", got)
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

func TestStartExecutesQueuedDurableReplacementTask(t *testing.T) {
	tasks := &previewTaskStore{}
	authFiles := &previewAuthFiles{files: []cpaauthfiles.File{
		proxyTestFile("runtime-1", "one.json", "1", "a@example.com", "http://old", false),
	}}
	service := newWorkerService(tasks, authFiles, nil)
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
	if _, err := service.Confirm(context.Background(), preview.Task.ID); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	service.Start(workerCtx)
	t.Cleanup(func() {
		cancel()
		if err := service.StopAndWait(context.Background()); err != nil {
			t.Fatalf("stop worker: %v", err)
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if tasks.task.Status == model.ProxyReplacementTaskCompleted && authFiles.proxyFor("runtime-1") == "http://new" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("queued task was not executed: task=%#v proxy=%q", tasks.task, authFiles.proxyFor("runtime-1"))
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
	if failed > 0 || skipped > 0 || recheckFailed > 0 {
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

type recordedPoolStateCall struct {
	proxyURL string
	reason   string
}

type recordingPoolState struct {
	states   []model.ProxyPoolState
	calls    []recordedPoolStateCall
	restored []string
}

type recordingInspectionResults struct {
	calls [][]model.ProxyReplacementTarget
}

func (s *recordingInspectionResults) ClearProxySuspects(_ context.Context, targets []model.ProxyReplacementTarget) error {
	s.calls = append(s.calls, append([]model.ProxyReplacementTarget(nil), targets...))
	return nil
}

func (s *recordingPoolState) List(context.Context) ([]model.ProxyPoolState, error) {
	return append([]model.ProxyPoolState(nil), s.states...), nil
}
func (s *recordingPoolState) MarkSuspect(_ context.Context, proxyURL, reason string) error {
	s.calls = append(s.calls, recordedPoolStateCall{proxyURL: proxyURL, reason: reason})
	return nil
}
func (s *recordingPoolState) Restore(_ context.Context, fingerprint string) (model.ProxyPoolState, error) {
	s.restored = append(s.restored, fingerprint)
	return model.ProxyPoolState{Fingerprint: fingerprint, Status: model.ProxyPoolStatusNormal}, nil
}

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
	fetchCalls            int
	failFullFetchAfter    int
	proxyResolveCalls     int
	patchCalls            int
	lastPatch             map[string]string
}

func (s *previewAuthFiles) Fetch(_ context.Context, baseURL, managementKey string) ([]cpaauthfiles.File, error) {
	s.fetchCalls++
	if s.expectedBaseURL != "" && (baseURL != s.expectedBaseURL || managementKey != s.expectedManagementKey) {
		return nil, errors.New("preview did not use the stored CPA connection")
	}
	if s.failFullFetchAfter > 0 && s.fetchCalls > s.failFullFetchAfter {
		return nil, errors.New("full inventory fetch must not run during task execution")
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
func (s *previewAuthFiles) ResolveProxyURLMutationTarget(_ context.Context, _ string, _ string, selector, authIndex string) (cpaauthfiles.StatusMutationTarget, error) {
	s.proxyResolveCalls++
	for _, file := range s.files {
		if file.ID == selector && (authIndex == "" || file.AuthIndex == authIndex) {
			return cpaauthfiles.StatusMutationTarget{Selector: selector, Scope: cpaauthfiles.StatusMutationScopeCredential, File: file}, nil
		}
	}
	return cpaauthfiles.StatusMutationTarget{}, errors.New("missing proxy lookup target")
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
	calls   int
	results []model.ProxyReplacementProbeResult
}

func (r *recordingRechecker) RecheckProxyReplacement(_ context.Context, targets []model.ProxyReplacementTarget, _ int) []model.ProxyReplacementProbeResult {
	r.calls++
	if r.results != nil {
		return append([]model.ProxyReplacementProbeResult(nil), r.results...)
	}
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
