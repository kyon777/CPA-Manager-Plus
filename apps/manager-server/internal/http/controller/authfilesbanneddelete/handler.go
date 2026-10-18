package authfilesbanneddelete

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
	authfilesbanneddeletesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesbanneddelete"
)

const (
	deleteAccountBannedBatchPath = "/v0/management/auth-files/delete-account-banned-batch"
	maxRequestBytes              = 64 * 1024
)

type deleteService interface {
	Delete(context.Context, []authfilesbanneddeletesvc.Target) (authfilesbanneddeletesvc.Result, error)
}

type Handler struct {
	App     *app.Context
	Service deleteService
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.App == nil || h.App.AdminAuthService == nil || h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("account-banned auth-file delete is unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if strings.TrimRight(r.URL.Path, "/") != deleteAccountBannedBatchPath {
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
		if errors.Is(err, authfilesbanneddeletesvc.ErrUnavailable) {
			status = http.StatusServiceUnavailable
		}
		response.Error(w, status, errors.New("account-banned auth-file delete failed"))
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func decodeTargets(reader io.Reader) ([]authfilesbanneddeletesvc.Target, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		return nil, errors.New("invalid account-banned auth-file delete request")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil || root == nil {
		return nil, errors.New("invalid account-banned auth-file delete request")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("invalid account-banned auth-file delete request")
	}
	if len(root) != 1 {
		return nil, errors.New("account-banned auth-file delete request contains an unsupported field")
	}
	rawTargets, ok := root["targets"]
	if !ok {
		return nil, errors.New("account-banned auth-file delete request requires targets")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(rawTargets, &values); err != nil || values == nil || len(values) > authfilesbanneddeletesvc.MaxTargets {
		return nil, errors.New("invalid account-banned auth-file delete targets")
	}
	targets := make([]authfilesbanneddeletesvc.Target, len(values))
	for index, value := range values {
		target, decodeErr := decodeTarget(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		targets[index] = target
	}
	return targets, nil
}

func decodeTarget(value json.RawMessage) (authfilesbanneddeletesvc.Target, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		return authfilesbanneddeletesvc.Target{}, errors.New("invalid account-banned auth-file delete target")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return authfilesbanneddeletesvc.Target{}, errors.New("invalid account-banned auth-file delete target")
	}
	allowed := map[string]struct{}{
		"clientKey": {}, "fileName": {}, "authIndex": {}, "accountEmail": {}, "provider": {}, "runtimeId": {}, "accountId": {}, "accountSnapshot": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return authfilesbanneddeletesvc.Target{}, errors.New("account-banned auth-file delete target contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return authfilesbanneddeletesvc.Target{}, errors.New("invalid account-banned auth-file delete target")
	}
	var target authfilesbanneddeletesvc.Target
	if err := json.Unmarshal(encoded, &target); err != nil {
		return authfilesbanneddeletesvc.Target{}, errors.New("invalid account-banned auth-file delete target")
	}
	target.ClientKey = strings.TrimSpace(target.ClientKey)
	target.FileName = strings.TrimSpace(target.FileName)
	target.AuthIndex = strings.TrimSpace(target.AuthIndex)
	target.AccountEmail = strings.TrimSpace(target.AccountEmail)
	target.Provider = strings.ToLower(strings.TrimSpace(target.Provider))
	target.RuntimeID = strings.TrimSpace(target.RuntimeID)
	target.AccountID = strings.TrimSpace(target.AccountID)
	target.AccountSnapshot = strings.TrimSpace(target.AccountSnapshot)
	if target.ClientKey == "" || target.FileName == "" || target.Provider != "codex" || target.AccountEmail == "" ||
		(target.AuthIndex == "" && target.RuntimeID == "") {
		return authfilesbanneddeletesvc.Target{}, errors.New("invalid account-banned auth-file delete target")
	}
	return target, nil
}
