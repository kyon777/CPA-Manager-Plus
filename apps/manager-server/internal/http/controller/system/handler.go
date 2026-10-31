package system

import (
	"context"
	"errors"
	"log"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
)

type Handler struct {
	App *app.Context
}

type dataMigrationStatus struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	LastEventID   int64  `json:"lastEventId"`
	TargetEventID int64  `json:"targetEventId"`
	ProcessedRows int64  `json:"processedRows"`
	ChangedRows   int64  `json:"changedRows"`
	AppliedRows   int64  `json:"appliedRows"`
	StartedAtMS   int64  `json:"startedAtMs,omitempty"`
	UpdatedAtMS   int64  `json:"updatedAtMs"`
	FinishedAtMS  int64  `json:"finishedAtMs,omitempty"`
}

type derivedStorageStreamStatus struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	CoverageEventID int64  `json:"coverageEventId"`
	TargetEventID   int64  `json:"targetEventId"`
	LagEventID      int64  `json:"lagEventId"`
	ProcessedEvents int64  `json:"processedEvents"`
	LastSuccessAtMS int64  `json:"lastSuccessAtMs,omitempty"`
	LastErrorCode   string `json:"lastErrorCode,omitempty"`
}

type derivedStorageStatus struct {
	Mode          string                       `json:"mode"`
	Enabled       bool                         `json:"enabled"`
	WorkerEnabled bool                         `json:"workerEnabled"`
	Degraded      bool                         `json:"degraded"`
	Streams       []derivedStorageStreamStatus `json:"streams"`
}

func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	info, err := h.App.SetupService.Info(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	response.JSON(w, http.StatusOK, info)
}

func (h *Handler) Status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	databaseMaintenance, err := h.App.Store.DerivedMaintenanceStatus(r.Context())
	if err != nil {
		log.Printf("read database maintenance status: %v", err)
		response.Error(w, http.StatusInternalServerError, errors.New("database maintenance status unavailable"))
		return
	}
	derivedStorage, err := derivedStatus(r.Context(), h.App)
	if err != nil {
		log.Printf("read separate derived storage status: %v", err)
		response.Error(w, http.StatusInternalServerError, errors.New("derived storage status unavailable"))
		return
	}
	if r.URL.Query().Get("scope") == "database-maintenance" {
		response.JSON(w, http.StatusOK, map[string]any{
			"databaseMaintenance": databaseMaintenance,
			"derivedStorage":      derivedStorage,
		})
		return
	}
	events, deadLetters, err := h.App.UsageService.Counts(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	status := h.App.CollectorService.Status()
	status.DeadLetters = deadLetters
	migration, err := h.App.Store.UsageCacheAccountingMigrationState(r.Context())
	if err != nil {
		response.Error(w, http.StatusInternalServerError, err)
		return
	}
	payload := map[string]any{
		"service":     h.App.ServiceID,
		"dbPath":      h.App.Config.DBPath,
		"events":      events,
		"deadLetters": deadLetters,
		"collector":   status,
		"dataMigration": dataMigrationStatus{
			Name:          migration.Name,
			Status:        migration.Status,
			LastEventID:   migration.LastEventID,
			TargetEventID: migration.TargetEventID,
			ProcessedRows: migration.ProcessedRows,
			ChangedRows:   migration.ChangedRows,
			AppliedRows:   migration.AppliedRows,
			StartedAtMS:   migration.StartedAtMS,
			UpdatedAtMS:   migration.UpdatedAtMS,
			FinishedAtMS:  migration.FinishedAtMS,
		},
		"databaseMaintenance": databaseMaintenance,
		"derivedStorage":      derivedStorage,
	}
	if h.App.DatabaseMaintenance != nil {
		payload["database"] = h.App.DatabaseMaintenance.Snapshot()
	}
	response.JSON(w, http.StatusOK, payload)
}

func derivedStatus(ctx context.Context, appCtx *app.Context) (derivedStorageStatus, error) {
	status := derivedStorageStatus{Mode: "primary", Streams: []derivedStorageStreamStatus{}}
	if appCtx == nil || appCtx.Store == nil || !appCtx.Store.SeparateDerivedEnabled() {
		return status, nil
	}
	derived, err := appCtx.Store.SeparateDerivedStatus(ctx)
	if err != nil {
		return derivedStorageStatus{}, err
	}
	status.Mode = "separate"
	status.Enabled = true
	status.WorkerEnabled = appCtx.Config.BackgroundDerivationsEnabled
	status.Degraded = !appCtx.Store.SeparateDerivedRuntimeAvailable()
	for _, stream := range derived.Streams {
		lag := stream.TargetEventID - stream.CoverageEventID
		if lag < 0 {
			lag = 0
		}
		streamStatus := stream.Status
		if lag > 0 && streamStatus == "ready" {
			streamStatus = "catching_up"
		}
		status.Streams = append(status.Streams, derivedStorageStreamStatus{
			Name:            string(stream.Stream),
			Status:          streamStatus,
			CoverageEventID: stream.CoverageEventID,
			TargetEventID:   stream.TargetEventID,
			LagEventID:      lag,
			ProcessedEvents: stream.ProcessedEvents,
			LastSuccessAtMS: stream.LastSuccessAtMS,
			LastErrorCode:   stream.LastErrorCode,
		})
	}
	return status, nil
}
