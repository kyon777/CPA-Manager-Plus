package credentialruntime

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	credentialruntimesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialruntime"
)

const (
	maxMetadataRequestBytes = 64 * 1024
	maxMetadataTargets      = 100
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.App == nil || h.App.CredentialRuntimeService == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("credential runtime service is unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	targets, err := decodeMetadataTargets(r)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	items, err := h.App.CredentialRuntimeService.Lookup(r.Context(), targets)
	if err != nil {
		response.Error(w, metadataErrorStatus(err), err)
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func decodeMetadataTargets(r *http.Request) ([]credentialruntimesvc.Target, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxMetadataRequestBytes+1))
	if err != nil || len(body) > maxMetadataRequestBytes {
		return nil, errors.New("invalid credential runtime metadata request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var rawRoot map[string]json.RawMessage
	if err := decoder.Decode(&rawRoot); err != nil || rawRoot == nil {
		return nil, errors.New("invalid credential runtime metadata request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid credential runtime metadata request")
	}
	if len(rawRoot) != 1 {
		return nil, errors.New("credential runtime metadata request contains an unsupported field")
	}
	rawTargets, ok := rawRoot["targets"]
	if !ok {
		return nil, errors.New("credential runtime metadata request requires targets")
	}
	var targetValues []json.RawMessage
	if err := json.Unmarshal(rawTargets, &targetValues); err != nil || targetValues == nil || len(targetValues) > maxMetadataTargets {
		return nil, errors.New("invalid credential runtime metadata targets")
	}

	targets := make([]credentialruntimesvc.Target, len(targetValues))
	for index, value := range targetValues {
		target, err := decodeMetadataTarget(value)
		if err != nil {
			return nil, err
		}
		targets[index] = target
	}
	return targets, nil
}

func decodeMetadataTarget(value json.RawMessage) (credentialruntimesvc.Target, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return credentialruntimesvc.Target{}, errors.New("invalid credential runtime metadata target")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return credentialruntimesvc.Target{}, errors.New("invalid credential runtime metadata target")
	}
	allowed := map[string]struct{}{
		"clientKey": {}, "fileName": {}, "authIndex": {}, "accountEmail": {}, "provider": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return credentialruntimesvc.Target{}, errors.New("credential runtime metadata target contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return credentialruntimesvc.Target{}, errors.New("invalid credential runtime metadata target")
	}
	var target credentialruntimesvc.Target
	if err := json.Unmarshal(encoded, &target); err != nil {
		return credentialruntimesvc.Target{}, errors.New("invalid credential runtime metadata target")
	}
	return target, nil
}

func metadataErrorStatus(err error) int {
	switch {
	case errors.Is(err, credentialruntimesvc.ErrRuntimeNotConfigured):
		return http.StatusPreconditionFailed
	case errors.Is(err, credentialruntimesvc.ErrRuntimeUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
