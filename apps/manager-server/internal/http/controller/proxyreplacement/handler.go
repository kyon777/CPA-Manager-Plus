package proxyreplacement

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	proxyreplacementsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxyreplacement"
)

const (
	basePath           = "/v0/management/proxy-replacements"
	maxPreviewBytes    = 1 << 20
	maxSmallRequestLen = 256 << 10
)

// Match a URL until a transport delimiter or a structured-message boundary.
// This intentionally also catches a URL enclosed in JSON quotes, which a
// whitespace-only scrubber would leave intact with proxy credentials exposed.
var urlRedactionPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>}\]]+`)

type Service interface {
	CreatePreview(context.Context, model.ProxyReplacementPreviewRequest) (model.ProxyReplacementTaskPage, error)
	Confirm(context.Context, int64) (model.ProxyReplacementTask, error)
	GetTask(context.Context, int64, int, int) (model.ProxyReplacementTaskPage, error)
	ListProxyPool(context.Context) ([]model.ProxyPoolState, error)
	RestoreProxyPoolEntry(context.Context, string) (model.ProxyPoolState, error)
	ReportInspectionSignal(context.Context, model.ProxyFailureEvidence) (bool, error)
}

type Handler struct {
	App     *app.Context
	Service Service
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.App == nil || h.App.AdminAuthService == nil || h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("proxy replacement service is unavailable"))
		return
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	switch {
	case path == basePath+"/previews":
		h.requireAdmin(w, r, h.preview)
	case path == basePath+"/inspection-signals":
		h.requireAdmin(w, r, h.inspectionSignal)
	case path == basePath+"/proxy-pool":
		h.requirePanel(w, r, h.listPool)
	case strings.HasPrefix(path, basePath+"/proxy-pool/") && strings.HasSuffix(path, "/restore"):
		h.requireAdmin(w, r, h.restorePool)
	case strings.HasPrefix(path, basePath+"/tasks/") && strings.HasSuffix(path, "/confirm"):
		h.requireAdmin(w, r, h.confirm)
	case strings.HasPrefix(path, basePath+"/tasks/"):
		h.requirePanel(w, r, h.getTask)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) inspectionSignal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	evidence, err := decodeInspectionSignal(http.MaxBytesReader(w, r.Body, maxSmallRequestLen))
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	marked, err := h.Service.ReportInspectionSignal(r.Context(), evidence)
	if err != nil {
		response.Error(w, previewErrorStatus(err), safeHandlerError(err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]bool{"marked": marked})
}

func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	if middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
		next(w, r)
	}
}

func (h *Handler) requirePanel(w http.ResponseWriter, r *http.Request, next func(http.ResponseWriter, *http.Request)) {
	if middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		next(w, r)
	}
}

func (h *Handler) preview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	request, err := decodePreviewRequest(http.MaxBytesReader(w, r.Body, maxPreviewBytes))
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	page, err := h.Service.CreatePreview(r.Context(), request)
	if err != nil {
		response.Error(w, previewErrorStatus(err), safeHandlerError(err))
		return
	}
	response.JSON(w, http.StatusOK, page)
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := decodeEmptyRequest(http.MaxBytesReader(w, r.Body, maxSmallRequestLen)); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	taskID, ok := taskIDFromPath(r.URL.Path, "/confirm")
	if !ok {
		http.NotFound(w, r)
		return
	}
	task, err := h.Service.Confirm(r.Context(), taskID)
	if err != nil {
		response.Error(w, previewErrorStatus(err), safeHandlerError(err))
		return
	}
	response.JSON(w, http.StatusAccepted, task)
}

func (h *Handler) getTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	taskID, ok := taskIDFromPath(r.URL.Path, "")
	if !ok {
		http.NotFound(w, r)
		return
	}
	page, pageSize, ok := parsePagination(r)
	if !ok {
		response.Error(w, http.StatusBadRequest, errors.New("invalid proxy replacement task pagination"))
		return
	}
	result, err := h.Service.GetTask(r.Context(), taskID, page, pageSize)
	if err != nil {
		response.Error(w, taskErrorStatus(err), safeHandlerError(err))
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func (h *Handler) listPool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	items, err := h.Service.ListProxyPool(r.Context())
	if err != nil {
		response.Error(w, http.StatusServiceUnavailable, safeHandlerError(err))
		return
	}
	response.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) restorePool(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}
	if err := decodeEmptyRequest(http.MaxBytesReader(w, r.Body, maxSmallRequestLen)); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	value := strings.TrimSuffix(strings.TrimPrefix(path, basePath+"/proxy-pool/"), "/restore")
	fingerprint := strings.TrimSpace(value)
	if fingerprint == "" || strings.Contains(fingerprint, "/") {
		response.Error(w, http.StatusBadRequest, errors.New("invalid proxy pool fingerprint"))
		return
	}
	state, err := h.Service.RestoreProxyPoolEntry(r.Context(), fingerprint)
	if err != nil {
		response.Error(w, taskErrorStatus(err), safeHandlerError(err))
		return
	}
	response.JSON(w, http.StatusOK, state)
}

func decodePreviewRequest(reader io.Reader) (model.ProxyReplacementPreviewRequest, error) {
	var root map[string]json.RawMessage
	if err := decodeOneJSON(reader, &root); err != nil || root == nil {
		return model.ProxyReplacementPreviewRequest{}, errors.New("invalid proxy replacement preview request")
	}
	allowed := map[string]struct{}{
		"source": {}, "mode": {}, "uniformProxyUrl": {}, "targets": {},
	}
	for key := range root {
		if _, ok := allowed[key]; !ok {
			return model.ProxyReplacementPreviewRequest{}, errors.New("proxy replacement preview request contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return model.ProxyReplacementPreviewRequest{}, errors.New("invalid proxy replacement preview request")
	}
	var request model.ProxyReplacementPreviewRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return model.ProxyReplacementPreviewRequest{}, errors.New("invalid proxy replacement preview request")
	}
	if err := validatePreviewTargets(root["targets"], &request); err != nil {
		return model.ProxyReplacementPreviewRequest{}, err
	}
	return request, nil
}

type inspectionSignalRequest struct {
	ProxyURL    string `json:"proxyUrl"`
	StatusCode  int    `json:"statusCode"`
	ErrorKind   string `json:"errorKind"`
	ErrorDetail string `json:"errorDetail"`
}

func decodeInspectionSignal(reader io.Reader) (model.ProxyFailureEvidence, error) {
	var root map[string]json.RawMessage
	if err := decodeOneJSON(reader, &root); err != nil || root == nil {
		return model.ProxyFailureEvidence{}, errors.New("invalid proxy inspection signal")
	}
	allowed := map[string]struct{}{
		"proxyUrl": {}, "statusCode": {}, "errorKind": {}, "errorDetail": {},
	}
	for key := range root {
		if _, ok := allowed[key]; !ok {
			return model.ProxyFailureEvidence{}, errors.New("proxy inspection signal contains an unsupported field")
		}
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return model.ProxyFailureEvidence{}, errors.New("invalid proxy inspection signal")
	}
	var request inspectionSignalRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return model.ProxyFailureEvidence{}, errors.New("invalid proxy inspection signal")
	}
	if strings.TrimSpace(request.ProxyURL) == "" {
		return model.ProxyFailureEvidence{}, errors.New("proxy inspection signal requires proxyUrl")
	}
	return model.ProxyFailureEvidence{
		ProxyURL:    request.ProxyURL,
		StatusCode:  request.StatusCode,
		ErrorKind:   request.ErrorKind,
		ErrorDetail: request.ErrorDetail,
	}, nil
}

func validatePreviewTargets(raw json.RawMessage, request *model.ProxyReplacementPreviewRequest) error {
	if request == nil || len(raw) == 0 {
		return errors.New("proxy replacement preview request requires targets")
	}
	var targets []json.RawMessage
	if err := json.Unmarshal(raw, &targets); err != nil || targets == nil {
		return errors.New("invalid proxy replacement targets")
	}
	if len(targets) != len(request.Targets) {
		return errors.New("invalid proxy replacement targets")
	}
	allowed := map[string]struct{}{
		"clientKey": {}, "fileName": {}, "authIndex": {}, "accountEmail": {}, "provider": {},
		"runtimeId": {}, "accountId": {}, "accountSnapshot": {},
	}
	for _, rawTarget := range targets {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(rawTarget, &object); err != nil || object == nil {
			return errors.New("invalid proxy replacement target")
		}
		for key := range object {
			if _, ok := allowed[key]; !ok {
				return errors.New("proxy replacement target contains an unsupported field")
			}
		}
	}
	return nil
}

func decodeEmptyRequest(reader io.Reader) error {
	if reader == nil {
		return nil
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return errors.New("invalid proxy replacement request")
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if err := decodeOneJSON(bytes.NewReader(body), &object); err != nil || len(object) != 0 {
		return errors.New("proxy replacement request must be empty")
	}
	return nil
}

func decodeOneJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	return expectEOF(decoder.Decode(&extra))
}

func expectEOF(err error) error {
	if err == io.EOF {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func taskIDFromPath(path, suffix string) (int64, bool) {
	path = strings.TrimRight(path, "/")
	if suffix != "" {
		if !strings.HasSuffix(path, suffix) {
			return 0, false
		}
		path = strings.TrimSuffix(path, suffix)
	}
	value := strings.TrimPrefix(path, basePath+"/tasks/")
	if value == path || value == "" || strings.Contains(value, "/") {
		return 0, false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func parsePagination(r *http.Request) (int, int, bool) {
	page := 1
	pageSize := 50
	query := r.URL.Query()
	if raw := strings.TrimSpace(query.Get("page")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			return 0, 0, false
		}
		page = value
	}
	if raw := strings.TrimSpace(query.Get("page_size")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			return 0, 0, false
		}
		pageSize = value
	}
	return page, pageSize, true
}

func previewErrorStatus(err error) int {
	switch {
	case errors.Is(err, proxyreplacementsvc.ErrUnavailable):
		return http.StatusServiceUnavailable
	case errors.Is(err, proxyreplacementsvc.ErrInvalidRequest), errors.Is(err, proxyreplacementsvc.ErrTaskExpired):
		return http.StatusBadRequest
	default:
		return http.StatusInternalServerError
	}
}

func taskErrorStatus(err error) int {
	if errors.Is(err, proxyreplacementsvc.ErrUnavailable) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, proxyreplacementsvc.ErrInvalidRequest) {
		return http.StatusBadRequest
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "missing task") {
		return http.StatusNotFound
	}
	return http.StatusInternalServerError
}

func safeHandlerError(err error) error {
	if err == nil {
		return errors.New("proxy replacement request failed")
	}
	message := redactURLs(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	return errors.New(message)
}

func redactURLs(message string) string {
	message = urlRedactionPattern.ReplaceAllString(message, "[redacted-url]")
	parts := strings.Fields(message)
	for index, value := range parts {
		trimmed := strings.TrimRight(value, ",.;:)]}")
		if strings.HasPrefix(strings.ToLower(trimmed), "http://") || strings.HasPrefix(strings.ToLower(trimmed), "https://") {
			parts[index] = strings.Replace(value, trimmed, "[redacted-url]", 1)
		}
	}
	return strings.Join(parts, " ")
}
