package authfilesselecteddelete

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	authfilesselecteddeletesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesselecteddelete"
)

const (
	deleteSelectedBatchPath = "/v0/management/auth-files/delete-selected-batch"
	maxRequestBytes         = 64 * 1024
)

type deleteService interface {
	Delete(context.Context, []authfilesselecteddeletesvc.Target) (authfilesselecteddeletesvc.Result, error)
}

type Handler struct {
	App     *app.Context
	Service deleteService
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.App == nil || h.App.AdminAuthService == nil || h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("selected auth-file delete is unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if strings.TrimRight(r.URL.Path, "/") != deleteSelectedBatchPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	targets, err := decodeTargets(r.Body)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.Service.Delete(r.Context(), targets)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, authfilesselecteddeletesvc.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		response.Error(w, status, errors.New("selected auth-file delete failed"))
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func decodeTargets(reader io.Reader) ([]authfilesselecteddeletesvc.Target, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		return nil, errors.New("invalid selected auth-file delete request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, errors.New("invalid selected auth-file delete request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid selected auth-file delete request")
	}
	if len(root) != 1 {
		return nil, errors.New("selected auth-file delete request contains an unsupported field")
	}
	rawTargets, ok := root["targets"]
	if !ok {
		return nil, errors.New("selected auth-file delete request requires targets")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(rawTargets, &values); err != nil || values == nil || len(values) > authfilesselecteddeletesvc.MaxTargets {
		return nil, errors.New("invalid selected auth-file delete targets")
	}
	targets := make([]authfilesselecteddeletesvc.Target, len(values))
	for index, value := range values {
		target, decodeErr := decodeTarget(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		targets[index] = target
	}
	return targets, nil
}

func decodeTarget(value json.RawMessage) (authfilesselecteddeletesvc.Target, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return authfilesselecteddeletesvc.Target{}, errors.New("invalid selected auth-file delete target")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return authfilesselecteddeletesvc.Target{}, errors.New("invalid selected auth-file delete target")
	}
	allowed := map[string]struct{}{
		"fileName": {}, "authIndex": {}, "provider": {}, "runtimeId": {}, "accountId": {}, "accountSnapshot": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return authfilesselecteddeletesvc.Target{}, errors.New("selected auth-file delete target contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return authfilesselecteddeletesvc.Target{}, errors.New("invalid selected auth-file delete target")
	}
	var target authfilesselecteddeletesvc.Target
	if err := json.Unmarshal(encoded, &target); err != nil {
		return authfilesselecteddeletesvc.Target{}, errors.New("invalid selected auth-file delete target")
	}
	target.FileName = strings.TrimSpace(target.FileName)
	target.AuthIndex = strings.TrimSpace(target.AuthIndex)
	target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
	target.RuntimeID = strings.TrimSpace(target.RuntimeID)
	target.AccountID = strings.TrimSpace(target.AccountID)
	target.AccountSnapshot = strings.TrimSpace(target.AccountSnapshot)
	if target.FileName == "" || target.Provider == "" || (target.AuthIndex == "" && target.RuntimeID == "") {
		return authfilesselecteddeletesvc.Target{}, errors.New("invalid selected auth-file delete target")
	}
	return target, nil
}
