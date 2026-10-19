package worker

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	servererrorpriorityrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/servererrorpriority"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestWorkerExecutesTenEventsAsTenPatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var state struct {
		sync.Mutex
		priority int
		targets  []int
	}
	state.priority = 30
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.Lock()
		defer state.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{durablePriorityTestFile("a.json", "a-index", "a-id", state.priority)})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			var body struct {
				Name     string `json:"name"`
				Priority int    `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode PATCH body: %v", err)
			}
			if body.Name != "a-id" {
				t.Fatalf("PATCH selector = %q, want a-id", body.Name)
			}
			state.targets = append(state.targets, body.Priority)
			state.priority = body.Priority
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	db := openDurablePriorityWorkerTestDB(t)
	queue := servererrorpriorityrepo.New(db)
	worker := NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver(
		queue,
		cpaauthfiles.NewMutationCoordinator(),
		serverErrorPriorityTestSetupResolver{setup: store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "test-key"}},
		ServerErrorPriorityDemotionOptions{Workers: 1, WakeBuffer: 64, ReconcileInterval: time.Hour},
	)
	worker.client = server.Client()
	if err := worker.Bootstrap(ctx, true); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	worker.Start(ctx)

	events := make([]usage.Event, 0, 10)
	for index := 0; index < 10; index++ {
		event := durablePriorityTestEvent(fmt.Sprintf("ten-%02d", index), "a.json", "a-index", "a@example.com")
		insertDurablePriorityWorkerEvent(t, db, event)
		events = append(events, event)
	}
	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, events)

	waitForDurablePriorityWorker(t, 3*time.Second, func() bool {
		return durablePriorityWorkerJobCount(t, db, "completed") == 10
	})

	state.Lock()
	got := append([]int(nil), state.targets...)
	state.Unlock()
	want := []int{29, 28, 27, 26, 25, 24, 23, 22, 21, 20}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("PATCH priority targets = %v, want %v", got, want)
	}
}

func TestWorkerSharesPeerInventoryFetchAcrossConcurrentJobs(t *testing.T) {
	var fullInventoryCalls atomic.Int32
	releaseInventory := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" || r.URL.Query().Get("name") != "" {
			http.NotFound(w, r)
			return
		}
		fullInventoryCalls.Add(1)
		<-releaseInventory
		_ = json.NewEncoder(w).Encode([]map[string]any{durablePriorityTestFile("a.json", "a-index", "a-id", 30)})
	}))
	t.Cleanup(server.Close)

	worker := NewServerErrorPriorityDemotionWorkerWithMutationCoordinator(cpaauthfiles.NewMutationCoordinator())
	worker.client = server.Client()
	candidate := serverErrorPriorityDemotionCandidate{
		BaseURL: server.URL, ManagementKey: "test-key", FileName: "a.json", AuthIndex: "a-index", Provider: "codex",
	}
	client := cpaauthfiles.New(server.Client(), time.Second)
	start := make(chan struct{})
	results := make(chan error, 8)
	for index := 0; index < 8; index++ {
		go func() {
			<-start
			_, err := worker.peerInventory(context.Background(), client, candidate)
			results <- err
		}()
	}
	close(start)

	deadline := time.Now().Add(500 * time.Millisecond)
	for fullInventoryCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if fullInventoryCalls.Load() == 0 {
		close(releaseInventory)
		t.Fatal("no inventory request reached CPA")
	}
	time.Sleep(100 * time.Millisecond)
	gotCalls := fullInventoryCalls.Load()
	close(releaseInventory)
	for index := 0; index < 8; index++ {
		if err := <-results; err != nil {
			t.Fatalf("peer inventory error: %v", err)
		}
	}
	if gotCalls != 1 {
		t.Fatalf("full inventory requests = %d, want one shared in-flight request", gotCalls)
	}
}

func TestWorkerHonorsConfiguredParallelismAndCredentialFIFO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var state struct {
		sync.Mutex
		priority  map[string]int
		active    int
		maxActive int
		started   []string
	}
	state.priority = map[string]int{"a-id": 30, "b-id": 30}
	patchStarted := make(chan string, 3)
	releasePatch := make(chan struct{}, 3)
	t.Cleanup(func() { close(releasePatch) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			state.Lock()
			defer state.Unlock()
			if name := r.URL.Query().Get("name"); name != "" {
				id := "a-id"
				index := "a-index"
				if name == "b.json" {
					id, index = "b-id", "b-index"
				}
				account := "a@example.com"
				if id == "b-id" {
					account = "b@example.com"
				}
				_ = json.NewEncoder(w).Encode([]map[string]any{durablePriorityTestFileForAccount(name, index, id, state.priority[id], account)})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				durablePriorityTestFileForAccount("a.json", "a-index", "a-id", state.priority["a-id"], "a@example.com"),
				durablePriorityTestFileForAccount("b.json", "b-index", "b-id", state.priority["b-id"], "b@example.com"),
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			var body struct {
				Name     string `json:"name"`
				Priority int    `json:"priority"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			state.Lock()
			state.active++
			if state.active > state.maxActive {
				state.maxActive = state.active
			}
			state.started = append(state.started, body.Name)
			state.Unlock()
			patchStarted <- body.Name
			<-releasePatch
			state.Lock()
			state.priority[body.Name] = body.Priority
			state.active--
			state.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	db := openDurablePriorityWorkerTestDB(t)
	queue := servererrorpriorityrepo.New(db)
	worker := NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver(
		queue,
		cpaauthfiles.NewMutationCoordinator(),
		serverErrorPriorityTestSetupResolver{setup: store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "test-key"}},
		ServerErrorPriorityDemotionOptions{Workers: 2, WakeBuffer: 64, ReconcileInterval: time.Hour},
	)
	worker.client = server.Client()
	if err := worker.Bootstrap(ctx, true); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	worker.Start(ctx)
	events := []usage.Event{
		durablePriorityTestEvent("fifo-a-1", "a.json", "a-index", "a@example.com"),
		durablePriorityTestEvent("fifo-a-2", "a.json", "a-index", "a@example.com"),
		durablePriorityTestEvent("fifo-b-1", "b.json", "b-index", "b@example.com"),
	}
	for _, event := range events {
		insertDurablePriorityWorkerEvent(t, db, event)
	}
	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, events)

	first, ok := receiveDurablePriorityPatch(patchStarted, time.Second)
	if !ok {
		t.Fatal("first PATCH did not start")
	}
	second, ok := receiveDurablePriorityPatch(patchStarted, time.Second)
	if !ok {
		releasePatch <- struct{}{}
		t.Fatalf("second independent credential PATCH did not start while first PATCH was blocked; jobs=%v", durablePriorityWorkerJobs(t, db))
	}
	if first == second {
		for index := 0; index < 2; index++ {
			releasePatch <- struct{}{}
		}
		t.Fatalf("first concurrent patches = %q, %q; second job for one credential must remain FIFO", first, second)
	}
	time.Sleep(100 * time.Millisecond)
	state.Lock()
	startedBeforeRelease := append([]string(nil), state.started...)
	maxActive := state.maxActive
	state.Unlock()
	if len(startedBeforeRelease) != 2 {
		for index := 0; index < len(startedBeforeRelease); index++ {
			releasePatch <- struct{}{}
		}
		t.Fatalf("PATCH calls before first credential settles = %v, want exactly two", startedBeforeRelease)
	}
	if maxActive != 2 {
		for index := 0; index < 2; index++ {
			releasePatch <- struct{}{}
		}
		t.Fatalf("maximum concurrent PATCH calls = %d, want 2", maxActive)
	}

	releasePatch <- struct{}{}
	releasePatch <- struct{}{}
	third, ok := receiveDurablePriorityPatch(patchStarted, time.Second)
	if !ok {
		t.Fatal("second same-credential PATCH did not start after first settled")
	}
	if third != "a-id" {
		releasePatch <- struct{}{}
		t.Fatalf("third PATCH selector = %q, want a-id after a-id first job settled", third)
	}
	releasePatch <- struct{}{}
	waitForDurablePriorityWorker(t, 3*time.Second, func() bool {
		return durablePriorityWorkerJobCount(t, db, "completed") == 3
	})
}

func TestWorkerDoesNotPatchAgainWhenRecoveredTargetAlreadyExists(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var patchCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{durablePriorityTestFile("a.json", "a-index", "a-id", 19)})
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/fields":
			patchCalls.Add(1)
			http.Error(w, "PATCH must not run after recovered target already exists", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	db := openDurablePriorityWorkerTestDB(t)
	queue := servererrorpriorityrepo.New(db)
	worker := NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver(
		queue,
		cpaauthfiles.NewMutationCoordinator(),
		serverErrorPriorityTestSetupResolver{setup: store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "test-key"}},
		ServerErrorPriorityDemotionOptions{Workers: 1, WakeBuffer: 64, ReconcileInterval: time.Hour},
	)
	worker.client = server.Client()
	if err := worker.Bootstrap(ctx, true); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	event := durablePriorityTestEvent("recovered-target", "a.json", "a-index", "a@example.com")
	insertDurablePriorityWorkerEvent(t, db, event)
	if _, err := queue.EnqueueEvents(ctx, []usage.Event{event}, 10); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	job, claimed, err := queue.ClaimNext(ctx, 100, 200, "abandoned-lease")
	if err != nil || !claimed {
		t.Fatalf("claim abandoned job = %#v, %t, %v", job, claimed, err)
	}
	if err := queue.RecordPriorityPlan(ctx, job.ID, job.LeaseToken, 20, 19, 101); err != nil {
		t.Fatalf("record abandoned plan: %v", err)
	}

	worker.Start(ctx)
	waitForDurablePriorityWorker(t, 3*time.Second, func() bool {
		return durablePriorityWorkerJobCount(t, db, "completed") == 1
	})
	if got := patchCalls.Load(); got != 0 {
		t.Fatalf("PATCH calls after recovered target matched = %d, want 0", got)
	}
	var reason string
	if err := db.QueryRow(`select coalesce(completed_reason, '') from server_error_priority_demotion_jobs where event_hash = ?`, event.EventHash).Scan(&reason); err != nil {
		t.Fatalf("read completed reason: %v", err)
	}
	if reason != "already_at_target" {
		t.Fatalf("completed reason = %q, want already_at_target", reason)
	}
}

func TestWorkerPersistsAllEventsWhenWakeBufferIsFull(t *testing.T) {
	ctx := context.Background()
	db := openDurablePriorityWorkerTestDB(t)
	queue := servererrorpriorityrepo.New(db)
	worker := NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver(
		queue,
		cpaauthfiles.NewMutationCoordinator(),
		serverErrorPriorityTestSetupResolver{setup: store.Setup{CPAUpstreamURL: "http://unused", ManagementKey: "unused"}},
		ServerErrorPriorityDemotionOptions{Workers: 1, WakeBuffer: 1},
	)
	if err := worker.Bootstrap(ctx, true); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	events := make([]usage.Event, 0, 32)
	for index := 0; index < 32; index++ {
		event := durablePriorityTestEvent(fmt.Sprintf("wake-full-%02d", index), "a.json", "a-index", "a@example.com")
		insertDurablePriorityWorkerEvent(t, db, event)
		events = append(events, event)
	}

	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, events)
	if got := durablePriorityWorkerJobCount(t, db, "pending"); got != len(events) {
		t.Fatalf("durably admitted jobs = %d, want all %d even with a saturated wake buffer", got, len(events))
	}
}

func TestWorkerEnableTransitionsNeverBackfillDisabledEvents(t *testing.T) {
	ctx := context.Background()
	db := openDurablePriorityWorkerTestDB(t)
	queue := servererrorpriorityrepo.New(db)
	worker := NewServerErrorPriorityDemotionWorkerWithQueueAndSetupResolver(
		queue,
		cpaauthfiles.NewMutationCoordinator(),
		nil,
		ServerErrorPriorityDemotionOptions{Workers: 1, WakeBuffer: 1},
	)
	if err := worker.Bootstrap(ctx, false); err != nil {
		t.Fatalf("bootstrap disabled: %v", err)
	}
	disabledBefore := durablePriorityTestEvent("disabled-before", "a.json", "a-index", "a@example.com")
	insertDurablePriorityWorkerEvent(t, db, disabledBefore)
	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{disabledBefore})

	if err := worker.SetEnabled(ctx, true); err != nil {
		t.Fatalf("enable: %v", err)
	}
	enabledFirst := durablePriorityTestEvent("enabled-first", "a.json", "a-index", "a@example.com")
	insertDurablePriorityWorkerEvent(t, db, enabledFirst)
	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{enabledFirst})

	if err := worker.SetEnabled(ctx, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	disabledMiddle := durablePriorityTestEvent("disabled-middle", "a.json", "a-index", "a@example.com")
	insertDurablePriorityWorkerEvent(t, db, disabledMiddle)
	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{disabledMiddle})

	if err := worker.SetEnabled(ctx, true); err != nil {
		t.Fatalf("reenable: %v", err)
	}
	enabledLast := durablePriorityTestEvent("enabled-last", "a.json", "a-index", "a@example.com")
	insertDurablePriorityWorkerEvent(t, db, enabledLast)
	worker.HandleUsageEvents(ctx, collectorpkg.RuntimeConfig{}, []usage.Event{enabledLast})
	if _, err := queue.Reconcile(ctx, 100, time.Now().UnixMilli()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	rows, err := db.Query(`select event_hash from server_error_priority_demotion_jobs order by event_hash`)
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	defer rows.Close()
	var hashes []string
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			t.Fatalf("scan job hash: %v", err)
		}
		hashes = append(hashes, hash)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate job hashes: %v", err)
	}
	if got, want := fmt.Sprint(hashes), fmt.Sprint([]string{"enabled-first", "enabled-last"}); got != want {
		t.Fatalf("admitted hashes = %s, want %s", got, want)
	}
}

func durablePriorityTestFile(name, authIndex, id string, priority int) map[string]any {
	return durablePriorityTestFileForAccount(name, authIndex, id, priority, "a@example.com")
}

func durablePriorityTestFileForAccount(name, authIndex, id string, priority int, account string) map[string]any {
	return map[string]any{
		"id": id, "name": name, "auth_index": authIndex, "provider": "codex",
		"email": account, "priority": priority,
	}
}

func openDurablePriorityWorkerTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func durablePriorityTestEvent(hash, fileName, authIndex, account string) usage.Event {
	return usage.Event{
		EventHash:        hash,
		TimestampMS:      100,
		Timestamp:        "1970-01-01T00:00:00Z",
		Provider:         "codex",
		AuthIndex:        authIndex,
		AccountSnapshot:  account,
		AuthFileSnapshot: fileName,
		Failed:           true,
		FailStatusCode:   http.StatusBadGateway,
		Model:            "gpt-5",
	}
}

func insertDurablePriorityWorkerEvent(t *testing.T, db *sql.DB, event usage.Event) {
	t.Helper()
	_, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_index,
		account_snapshot, auth_file_snapshot, failed, fail_status_code, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventHash, event.TimestampMS, event.Timestamp, event.Provider, event.Model,
		event.AuthIndex, event.AccountSnapshot, event.AuthFileSnapshot, 1,
		event.FailStatusCode, event.TimestampMS,
	)
	if err != nil {
		t.Fatalf("insert usage event %q: %v", event.EventHash, err)
	}
}

func durablePriorityWorkerJobCount(t *testing.T, db *sql.DB, state string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from server_error_priority_demotion_jobs where state = ?`, state).Scan(&count); err != nil {
		t.Fatalf("count %s priority jobs: %v", state, err)
	}
	return count
}

func durablePriorityWorkerJobs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`select event_hash, state, credential_key, attempt_count, coalesce(last_error_code, '') from server_error_priority_demotion_jobs order by id`)
	if err != nil {
		t.Fatalf("list priority jobs: %v", err)
	}
	defer rows.Close()
	jobs := make([]string, 0)
	for rows.Next() {
		var hash, state, key, code string
		var attempts int
		if err := rows.Scan(&hash, &state, &key, &attempts, &code); err != nil {
			t.Fatalf("scan priority job: %v", err)
		}
		jobs = append(jobs, fmt.Sprintf("%s:%s:%s:%d:%s", hash, state, key, attempts, code))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate priority jobs: %v", err)
	}
	return jobs
}

func waitForDurablePriorityWorker(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition was not satisfied within %s", timeout)
}

func receiveDurablePriorityPatch(started <-chan string, timeout time.Duration) (string, bool) {
	select {
	case value := <-started:
		return value, true
	case <-time.After(timeout):
		return "", false
	}
}

func sortedDurablePriorityInts(values []int) []int {
	cloned := append([]int(nil), values...)
	sort.Ints(cloned)
	return cloned
}
