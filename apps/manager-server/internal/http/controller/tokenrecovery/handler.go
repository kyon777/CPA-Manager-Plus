package tokenrecovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	tokenrecoveryrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/tokenrecovery"
	tokenrecoverysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/tokenrecovery"
)

const maxTargetRequestBytes = 16 * 1024

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.App == nil || h.App.TokenRecoveryService == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("token recovery service is unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	path := strings.TrimRight(strings.TrimSpace(r.URL.Path), "/")
	switch path {
	case "/v0/management/token-recovery/signals":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		target, err := decodeTarget(r)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		task, err := h.App.TokenRecoveryService.SignalAutomatic(r.Context(), target)
		if err != nil {
			response.Error(w, tokenRecoveryErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"task": task})
	case "/v0/management/token-recovery/manual":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		target, err := decodeTarget(r)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		task, err := h.App.TokenRecoveryService.RequestManual(r.Context(), target)
		if err != nil {
			response.Error(w, tokenRecoveryErrorStatus(err), err)
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"task": task})
	case "/v0/management/token-recovery":
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		target := targetFromQuery(r)
		task, found, err := h.App.TokenRecoveryService.Get(r.Context(), target)
		if err != nil {
			response.Error(w, tokenRecoveryErrorStatus(err), err)
			return
		}
		if !found {
			response.JSON(w, http.StatusOK, map[string]any{"task": nil})
			return
		}
		response.JSON(w, http.StatusOK, map[string]any{"task": task})
	default:
		response.MethodNotAllowed(w)
	}
}

func decodeTarget(r *http.Request) (model.TokenRecoveryTarget, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxTargetRequestBytes+1))
	if err != nil || len(body) > maxTargetRequestBytes {
		return model.TokenRecoveryTarget{}, errors.New("invalid token recovery request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return model.TokenRecoveryTarget{}, errors.New("invalid token recovery request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return model.TokenRecoveryTarget{}, errors.New("invalid token recovery request")
	}
	allowed := map[string]struct{}{
		"fileName": {}, "authIndex": {}, "accountEmail": {}, "provider": {}, "observedAtMs": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return model.TokenRecoveryTarget{}, errors.New("token recovery request contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return model.TokenRecoveryTarget{}, errors.New("invalid token recovery request")
	}
	var target model.TokenRecoveryTarget
	if err := json.Unmarshal(encoded, &target); err != nil {
		return model.TokenRecoveryTarget{}, errors.New("invalid token recovery request")
	}
	if strings.TrimSpace(target.Provider) == "" {
		target.Provider = "codex"
	}
	return target, nil
}

func targetFromQuery(r *http.Request) model.TokenRecoveryTarget {
	query := r.URL.Query()
	provider := strings.TrimSpace(query.Get("provider"))
	if provider == "" {
		provider = "codex"
	}
	return model.TokenRecoveryTarget{
		FileName:     query.Get("fileName"),
		AuthIndex:    query.Get("authIndex"),
		AccountEmail: query.Get("accountEmail"),
		Provider:     provider,
	}
}

func tokenRecoveryErrorStatus(err error) int {
	switch {
	case errors.Is(err, tokenrecoveryrepo.ErrInvalidTarget):
		return http.StatusBadRequest
	case errors.Is(err, tokenrecoverysvc.ErrRecoveryNotConfigured):
		return http.StatusPreconditionFailed
	case errors.Is(err, tokenrecoverysvc.ErrRecoveryUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
