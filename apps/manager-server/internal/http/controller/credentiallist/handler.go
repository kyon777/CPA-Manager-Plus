package credentiallist

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	credentiallistsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentiallist"
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if strings.TrimRight(r.URL.Path, "/") != "/usage-service/credential-list" {
		response.MethodNotAllowed(w)
		return
	}
	if r.Method != http.MethodGet {
		response.MethodNotAllowed(w)
		return
	}
	if h.App.CredentialListService == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("credential list is unavailable"))
		return
	}
	request, err := parseRequest(r.URL.Query())
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	result, err := h.App.CredentialListService.List(r.Context(), request)
	if err != nil {
		response.Error(w, http.StatusBadGateway, err)
		return
	}
	response.JSON(w, http.StatusOK, result)
}

func parseRequest(query url.Values) (credentiallistsvc.Request, error) {
	request := credentiallistsvc.Request{
		Page:            1,
		PageSize:        50,
		Search:          query.Get("search"),
		Provider:        query.Get("provider"),
		Status:          query.Get("status"),
		Sort:            query.Get("sort"),
		IncludeProxyURL: true,
	}
	if value := strings.TrimSpace(query.Get("page")); value != "" {
		page, err := strconv.Atoi(value)
		if err != nil || page < 1 {
			return credentiallistsvc.Request{}, errInvalidPage
		}
		request.Page = page
	}
	if value := strings.TrimSpace(query.Get("page_size")); value != "" {
		pageSize, err := strconv.Atoi(value)
		if err != nil || pageSize < 1 || pageSize > 50 {
			return credentiallistsvc.Request{}, errInvalidPageSize
		}
		request.PageSize = pageSize
	}
	if value := strings.TrimSpace(query.Get("include_proxy_url")); value != "" {
		request.IncludeProxyURL = value == "1" || strings.EqualFold(value, "true")
	}
	return request, nil
}

var (
	errInvalidPage     = &queryError{"page must be a positive integer"}
	errInvalidPageSize = &queryError{"page_size must be between 1 and 50"}
)

type queryError struct{ message string }

func (e *queryError) Error() string { return e.message }
