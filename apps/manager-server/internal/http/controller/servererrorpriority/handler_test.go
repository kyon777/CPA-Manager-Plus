package servererrorpriority

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	adminauthsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/adminauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const handlerAdminKey = "cpamp_server_error_priority_updates"

type recordingRepository struct {
	page   model.ServerErrorPriorityDemotionUpdatePage
	cursor model.ServerErrorPriorityDemotionCursor
	limit  int
}

func (r *recordingRepository) ListCompletedDemotions(
	_ context.Context,
	cursor model.ServerErrorPriorityDemotionCursor,
	limit int,
) (model.ServerErrorPriorityDemotionUpdatePage, error) {
	r.cursor = cursor
	r.limit = limit
	return r.page, nil
}

func TestHandlerReturnsOnlyRedactedCompletedDemotionUpdates(t *testing.T) {
	repo := &recordingRepository{page: model.ServerErrorPriorityDemotionUpdatePage{
		Items: []model.ServerErrorPriorityDemotionUpdate{{
			ID:             42,
			FileName:       "account.json",
			AuthIndex:      "7",
			Provider:       "codex",
			PriorityBefore: 27004,
			PriorityTarget: 27003,
			CompletedAtMS:  1001,
		}},
		NextCursor: model.ServerErrorPriorityDemotionCursor{CompletedAtMS: 1001, ID: 42},
	}}
	handler := newTestHandler(t, repo)
	request := httptest.NewRequest(http.MethodGet, "/v0/management/server-error-priority-demotions/updates?after_completed_at_ms=1000&after_id=10&limit=2", nil)
	request.Header.Set("Authorization", "Bearer "+handlerAdminKey)
	recorder := httptest.NewRecorder()

	handler.Handle(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if repo.cursor != (model.ServerErrorPriorityDemotionCursor{CompletedAtMS: 1000, ID: 10}) || repo.limit != 2 {
		t.Fatalf("repository request = %#v limit=%d", repo.cursor, repo.limit)
	}
	var response model.ServerErrorPriorityDemotionUpdatePage
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Items) != 1 || response.Items[0].ID != 42 || response.Items[0].PriorityBefore != 27004 || response.NextCursor.ID != 42 {
		t.Fatalf("response = %#v", response)
	}
	for _, forbidden := range []string{"accountSnapshot", "eventHash", "proxy", "access_token", "management-key"} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestHandlerRejectsInvalidCursorLimitAndMethod(t *testing.T) {
	handler := newTestHandler(t, &recordingRepository{})
	for _, target := range []string{
		"/v0/management/server-error-priority-demotions/updates?after_completed_at_ms=-1",
		"/v0/management/server-error-priority-demotions/updates?after_id=-1",
		"/v0/management/server-error-priority-demotions/updates?limit=0",
		"/v0/management/server-error-priority-demotions/updates?limit=201",
		"/v0/management/server-error-priority-demotions/updates?limit=bad",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.Header.Set("Authorization", "Bearer "+handlerAdminKey)
		recorder := httptest.NewRecorder()
		handler.Handle(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("target %q status=%d body=%s", target, recorder.Code, recorder.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v0/management/server-error-priority-demotions/updates", nil)
	request.Header.Set("Authorization", "Bearer "+handlerAdminKey)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandlerRequiresPanelAuthorization(t *testing.T) {
	handler := newTestHandler(t, &recordingRepository{})
	request := httptest.NewRequest(http.MethodGet, "/v0/management/server-error-priority-demotions/updates", nil)
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func newTestHandler(t *testing.T, repo *recordingRepository) *Handler {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "server-error-priority-handler.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	credential, err := security.NewAdminCredential(handlerAdminKey, "test")
	if err != nil {
		t.Fatalf("new admin credential: %v", err)
	}
	if err := st.SaveAdminCredential(context.Background(), credential); err != nil {
		t.Fatalf("save admin credential: %v", err)
	}
	return &Handler{
		App:        &app.Context{AdminAuthService: adminauthsvc.New(config.Config{}, st)},
		Repository: repo,
	}
}
