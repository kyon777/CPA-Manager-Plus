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

const (
	maxTargetRequestBytes      = 16 * 1024
	maxBatchTargetRequestBytes = 64 * 1024
	maxBatchTargets            = 100
)

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
	case "/v0/management/token-recovery/query":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		batchTargets, err := decodeBatchTargets(r)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		items := make([]batchItem, len(batchTargets))
		for index, batchTarget := range batchTargets {
			items[index].ClientKey = batchTarget.ClientKey
			task, found, err := h.App.TokenRecoveryService.Get(r.Context(), batchTarget.Target)
			if err != nil {
				items[index].ErrorCode = tokenRecoveryItemErrorCode(err)
				continue
			}
			if found {
				items[index].Task = &task
			}
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": items})
	case "/v0/management/token-recovery/manual/batch":
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		batchTargets, err := decodeBatchTargets(r)
		if err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		items := make([]batchItem, len(batchTargets))
		for index, batchTarget := range batchTargets {
			items[index].ClientKey = batchTarget.ClientKey
			task, err := h.App.TokenRecoveryService.RequestManual(r.Context(), batchTarget.Target)
			if err != nil {
				items[index].ErrorCode = tokenRecoveryItemErrorCode(err)
				continue
			}
			items[index].Task = &task
		}
		response.JSON(w, http.StatusOK, map[string]any{"items": items})
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

type batchTarget struct {
	ClientKey string
	Target    model.TokenRecoveryTarget
}

type batchItem struct {
	ClientKey string                   `json:"clientKey"`
	Task      *model.TokenRecoveryTask `json:"task"`
	ErrorCode string                   `json:"errorCode,omitempty"`
}

func decodeBatchTargets(r *http.Request) ([]batchTarget, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBatchTargetRequestBytes+1))
	if err != nil || len(body) > maxBatchTargetRequestBytes {
		return nil, errors.New("invalid token recovery batch request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var rawRoot map[string]json.RawMessage
	if err := decoder.Decode(&rawRoot); err != nil || rawRoot == nil {
		return nil, errors.New("invalid token recovery batch request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid token recovery batch request")
	}
	if len(rawRoot) != 1 {
		return nil, errors.New("token recovery batch request contains an unsupported field")
	}
	rawTargets, ok := rawRoot["targets"]
	if !ok {
		return nil, errors.New("token recovery batch request requires targets")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(rawTargets, &values); err != nil || values == nil || len(values) > maxBatchTargets {
		return nil, errors.New("invalid token recovery batch targets")
	}
	targets := make([]batchTarget, len(values))
	for index, value := range values {
		target, err := decodeBatchTarget(value)
		if err != nil {
			return nil, err
		}
		targets[index] = target
	}
	return targets, nil
}

func decodeBatchTarget(value json.RawMessage) (batchTarget, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return batchTarget{}, errors.New("invalid token recovery batch target")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return batchTarget{}, errors.New("invalid token recovery batch target")
	}
	allowed := map[string]struct{}{
		"clientKey": {}, "fileName": {}, "authIndex": {}, "accountEmail": {}, "provider": {}, "observedAtMs": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return batchTarget{}, errors.New("token recovery batch target contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return batchTarget{}, errors.New("invalid token recovery batch target")
	}
	var decoded struct {
		ClientKey    string `json:"clientKey"`
		FileName     string `json:"fileName"`
		AuthIndex    string `json:"authIndex"`
		AccountEmail string `json:"accountEmail"`
		Provider     string `json:"provider"`
		ObservedAtMS int64  `json:"observedAtMs"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return batchTarget{}, errors.New("invalid token recovery batch target")
	}
	provider := strings.TrimSpace(decoded.Provider)
	if provider == "" {
		provider = "codex"
	}
	return batchTarget{
		ClientKey: strings.TrimSpace(decoded.ClientKey),
		Target: model.TokenRecoveryTarget{
			FileName:     decoded.FileName,
			AuthIndex:    decoded.AuthIndex,
			AccountEmail: decoded.AccountEmail,
			Provider:     provider,
			ObservedAtMS: decoded.ObservedAtMS,
		},
	}, nil
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

func tokenRecoveryItemErrorCode(err error) string {
	switch {
	case errors.Is(err, tokenrecoveryrepo.ErrInvalidTarget):
		return "invalid_target"
	case errors.Is(err, tokenrecoverysvc.ErrRecoveryNotConfigured):
		return "not_configured"
	case errors.Is(err, tokenrecoverysvc.ErrRecoveryUnavailable):
		return "recovery_unavailable"
	default:
		return "recovery_failed"
	}
}
