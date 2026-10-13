package authfilesbatch

import (
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	batchsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesbatch"
)

type Handler struct {
	App     *app.Context
	Service *batchsvc.Service
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	path := strings.TrimRight(r.URL.Path, "/")
	const prefix = "/v0/management/auth-files/batch-upload"
	if path == prefix {
		if r.Method != http.MethodPost {
			response.MethodNotAllowed(w)
			return
		}
		h.start(w, r)
		return
	}
	if strings.HasPrefix(path, prefix+"/") {
		if r.Method != http.MethodGet {
			response.MethodNotAllowed(w)
			return
		}
		h.status(w, r, strings.TrimPrefix(path, prefix+"/"))
		return
	}
	http.NotFound(w, r)
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	if h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("auth-file batch upload is unavailable"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, batchsvc.MaxTotalSize+1024*1024)
	if err := r.ParseMultipartForm(batchsvc.MaxTotalSize + 1024*1024); err != nil {
		response.Error(w, http.StatusRequestEntityTooLarge, errors.New("invalid or oversized auth-file batch"))
		return
	}
	if r.MultipartForm == nil {
		response.Error(w, http.StatusBadRequest, errors.New("at least one auth file is required"))
		return
	}
	defer r.MultipartForm.RemoveAll()
	parts := r.MultipartForm.File["files"]
	if len(parts) == 0 {
		// Accept the conventional singular form field too, so a non-JS client can
		// use the same endpoint without changing its form naming.
		parts = r.MultipartForm.File["file"]
	}
	files, err := readFiles(parts)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	status, err := h.Service.Start(r.Context(), files)
	if err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	response.JSON(w, http.StatusAccepted, status)
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request, id string) {
	if h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("auth-file batch upload is unavailable"))
		return
	}
	status, found := h.Service.Get(id)
	if !found {
		response.Error(w, http.StatusNotFound, errors.New("auth-file batch upload not found"))
		return
	}
	response.JSON(w, http.StatusOK, status)
}

func readFiles(parts []*multipart.FileHeader) ([]batchsvc.UploadFile, error) {
	if len(parts) == 0 {
		return nil, errors.New("at least one auth file is required")
	}
	if len(parts) > batchsvc.MaxFiles {
		return nil, errors.New("too many auth files")
	}
	files := make([]batchsvc.UploadFile, 0, len(parts))
	total := 0
	for _, part := range parts {
		if part == nil {
			return nil, errors.New("invalid auth file")
		}
		file, err := part.Open()
		if err != nil {
			return nil, errors.New("read auth file")
		}
		data, readErr := io.ReadAll(io.LimitReader(file, batchsvc.MaxTotalSize+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.New("read auth file")
		}
		total += len(data)
		if total > batchsvc.MaxTotalSize {
			return nil, errors.New("auth-file batch is too large")
		}
		files = append(files, batchsvc.UploadFile{Name: part.Filename, Data: data})
	}
	return files, nil
}
