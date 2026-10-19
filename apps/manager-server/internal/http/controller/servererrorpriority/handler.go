// Package servererrorpriority exposes redacted, completed durable priority
// demotions to the Manager UI. It never forwards this endpoint to CPA Core.
package servererrorpriority

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

const updatesPath = "/v0/management/server-error-priority-demotions/updates"

const (
	defaultUpdateLimit = 200
	maxUpdateLimit     = 200
)

// Repository is the narrow read-only projection required by this endpoint.
type Repository interface {
	ListCompletedDemotions(
		context.Context,
		model.ServerErrorPriorityDemotionCursor,
		int,
	) (model.ServerErrorPriorityDemotionUpdatePage, error)
}

type Handler struct {
	App        *app.Context
	Repository Repository
}

func New(appCtx *app.Context) *Handler {
	handler := &Handler{App: appCtx}
	if appCtx != nil && appCtx.Store != nil {
		handler.Repository = appCtx.Store.ServerErrorPriorityDemotions
	}
	return handler
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.App == nil || h.App.AdminAuthService == nil || h.Repository == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("server-error priority updates unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	cursor, limit, err := parseQuery(r)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	page, err := h.Repository.ListCompletedDemotions(r.Context(), cursor, limit)
	if err != nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("server-error priority updates unavailable"))
		return
	}
	response.JSON(w, http.StatusOK, page)
}

func parseQuery(r *http.Request) (model.ServerErrorPriorityDemotionCursor, int, error) {
	if r == nil {
		return model.ServerErrorPriorityDemotionCursor{}, 0, errors.New("invalid priority update query")
	}
	completedAtMS, err := parseNonNegativeInt64(r.URL.Query().Get("after_completed_at_ms"))
	if err != nil {
		return model.ServerErrorPriorityDemotionCursor{}, 0, errors.New("invalid after_completed_at_ms")
	}
	id, err := parseNonNegativeInt64(r.URL.Query().Get("after_id"))
	if err != nil {
		return model.ServerErrorPriorityDemotionCursor{}, 0, errors.New("invalid after_id")
	}
	limit := defaultUpdateLimit
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > maxUpdateLimit {
			return model.ServerErrorPriorityDemotionCursor{}, 0, errors.New("invalid limit")
		}
		limit = parsed
	}
	return model.ServerErrorPriorityDemotionCursor{CompletedAtMS: completedAtMS, ID: id}, limit, nil
}

func parseNonNegativeInt64(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("invalid non-negative integer")
	}
	return parsed, nil
}
