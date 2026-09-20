package proxyfilter

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// Handler serves the server-side proxy URL inventory used by the panel's
// missing-proxy filter. It intentionally stores only the normalized URL list;
// credential files remain owned by CPA Core.
type Handler struct {
	App *app.Context
}

type updateRequest struct {
	URLs []string `json:"urls"`
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.TrimRight(r.URL.Path, "/") != "/usage-service/proxy-filter" {
		response.MethodNotAllowed(w)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
			return
		}
		settings, present, err := h.App.Store.LoadProxyFilterSettings(r.Context())
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		if !present {
			settings = model.ProxyFilterSettings{URLs: []string{}}
		}
		response.JSON(w, http.StatusOK, settings)
	case http.MethodPut:
		if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
			return
		}
		var req updateRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20))
		if err := decoder.Decode(&req); err != nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		saved, err := h.App.Store.SaveProxyFilterSettings(r.Context(), model.ProxyFilterSettings{URLs: req.URLs})
		if err != nil {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		response.JSON(w, http.StatusOK, saved)
	default:
		response.MethodNotAllowed(w)
	}
}
