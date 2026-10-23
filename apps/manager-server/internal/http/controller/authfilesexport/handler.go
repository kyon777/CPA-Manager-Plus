// Package authfilesexport exposes the narrow authenticated boundary for a
// selected auth-file JSON export. The request contains identity fields only;
// raw credential data can travel only from CPA to the authenticated response.
package authfilesexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	authfilesexportsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/authfilesexport"
)

const (
	exportSelectedPath = "/v0/management/auth-files/export-selected"
	maxRequestBytes    = 512 * 1024
)

var errInvalidRequest = errors.New("invalid selected auth JSON export request")

type exportService interface {
	Export(context.Context, []authfilesexportsvc.Target) (authfilesexportsvc.Result, error)
}

type Handler struct {
	App     *app.Context
	Service exportService
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	// Auth JSON is credential material. Mark every response path, including
	// authorization and validation failures, non-cacheable before doing work.
	w.Header().Set("Cache-Control", "no-store")
	if h == nil || h.App == nil || h.App.AdminAuthService == nil || h.Service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("selected auth JSON export is unavailable"))
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if strings.TrimRight(r.URL.Path, "/") != exportSelectedPath {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		response.MethodNotAllowed(w)
		return
	}

	targets, err := decodeTargets(r.Body)
	if err != nil {
		response.Error(w, http.StatusBadRequest, errInvalidRequest)
		return
	}
	result, err := h.Service.Export(r.Context(), targets)
	switch {
	case errors.Is(err, authfilesexportsvc.ErrUnavailable):
		response.Error(w, http.StatusServiceUnavailable, errors.New("selected auth JSON export is unavailable"))
	case errors.Is(err, authfilesexportsvc.ErrTooManyTargets), errors.Is(err, authfilesexportsvc.ErrExportLimitExceeded):
		response.JSON(w, http.StatusRequestEntityTooLarge, map[string]string{"code": "export_limit_exceeded"})
	case err != nil:
		response.Error(w, http.StatusInternalServerError, errors.New("selected auth JSON export failed"))
	default:
		response.JSON(w, http.StatusOK, result)
	}
}

// decodeTargets accepts one bounded JSON object whose sole root member is
// targets. It rejects unknown and duplicate fields before any request value can
// reach the service layer.
func decodeTargets(reader io.Reader) ([]authfilesexportsvc.Target, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxRequestBytes+1))
	if err != nil || len(body) > maxRequestBytes {
		return nil, fmt.Errorf("%w: body", errInvalidRequest)
	}
	root, err := decodeSingleJSONObject(body)
	if err != nil || len(root) != 1 {
		return nil, fmt.Errorf("%w: root", errInvalidRequest)
	}
	rawTargets, ok := root["targets"]
	if !ok {
		return nil, fmt.Errorf("%w: targets", errInvalidRequest)
	}
	var values []json.RawMessage
	if err := json.Unmarshal(rawTargets, &values); err != nil || values == nil || len(values) > authfilesexportsvc.MaxTargets {
		return nil, fmt.Errorf("%w: targets", errInvalidRequest)
	}

	targets := make([]authfilesexportsvc.Target, len(values))
	seenClientKeys := make(map[string]struct{}, len(values))
	for index, value := range values {
		target, decodeErr := decodeTarget(value)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if _, exists := seenClientKeys[target.ClientKey]; exists {
			return nil, fmt.Errorf("%w: duplicate client key", errInvalidRequest)
		}
		seenClientKeys[target.ClientKey] = struct{}{}
		targets[index] = target
	}
	return targets, nil
}

func decodeTarget(value json.RawMessage) (authfilesexportsvc.Target, error) {
	raw, err := decodeSingleJSONObject(value)
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: target", errInvalidRequest)
	}
	allowed := map[string]struct{}{
		"clientKey": {}, "fileName": {}, "authIndex": {}, "accountEmail": {},
		"provider": {}, "runtimeId": {}, "accountId": {}, "accountSnapshot": {},
	}
	for key := range raw {
		if _, ok := allowed[key]; !ok {
			return authfilesexportsvc.Target{}, fmt.Errorf("%w: unsupported target field", errInvalidRequest)
		}
	}

	readString := func(key string) (string, error) {
		value, present := raw[key]
		if !present {
			return "", nil
		}
		var decoded string
		if err := json.Unmarshal(value, &decoded); err != nil {
			return "", err
		}
		return decoded, nil
	}
	clientKey, err := readString("clientKey")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: client key", errInvalidRequest)
	}
	fileName, err := readString("fileName")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: file name", errInvalidRequest)
	}
	authIndex, err := readString("authIndex")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: auth index", errInvalidRequest)
	}
	accountEmail, err := readString("accountEmail")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: account email", errInvalidRequest)
	}
	provider, err := readString("provider")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: provider", errInvalidRequest)
	}
	runtimeID, err := readString("runtimeId")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: runtime id", errInvalidRequest)
	}
	accountID, err := readString("accountId")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: account id", errInvalidRequest)
	}
	accountSnapshot, err := readString("accountSnapshot")
	if err != nil {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: account snapshot", errInvalidRequest)
	}

	target := authfilesexportsvc.Target{
		ClientKey:       strings.TrimSpace(clientKey),
		FileName:        strings.TrimSpace(fileName),
		AuthIndex:       strings.TrimSpace(authIndex),
		AccountEmail:    strings.ToLower(strings.TrimSpace(accountEmail)),
		Provider:        strings.ToLower(strings.TrimSpace(provider)),
		RuntimeID:       strings.TrimSpace(runtimeID),
		AccountID:       strings.TrimSpace(accountID),
		AccountSnapshot: strings.TrimSpace(accountSnapshot),
	}
	if target.ClientKey == "" || target.FileName == "" || target.Provider == "" ||
		(target.AuthIndex == "" && target.RuntimeID == "") {
		return authfilesexportsvc.Target{}, fmt.Errorf("%w: incomplete target", errInvalidRequest)
	}
	return target, nil
}

// decodeSingleJSONObject both rejects duplicate keys and proves that the input
// contains exactly one complete JSON object (rather than a valid object plus a
// second JSON document).
func decodeSingleJSONObject(input []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(input))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("JSON root is not an object")
	}

	values := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, keyErr := decoder.Token()
		if keyErr != nil {
			return nil, keyErr
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("JSON object key is invalid")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, errors.New("JSON object has duplicate key")
		}
		var raw json.RawMessage
		if decodeErr := decoder.Decode(&raw); decodeErr != nil {
			return nil, decodeErr
		}
		values[key] = raw
	}
	if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
		if closeErr != nil {
			return nil, closeErr
		}
		return nil, errors.New("JSON object terminator is invalid")
	}
	if _, extraErr := decoder.Token(); extraErr != io.EOF {
		if extraErr == nil {
			return nil, errors.New("second JSON document")
		}
		return nil, extraErr
	}
	return values, nil
}
