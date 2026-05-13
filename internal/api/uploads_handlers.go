package api

import (
	"net/http"

	"github.com/Compdeep/kaiju/internal/agent/uploads"
	"github.com/Compdeep/kaiju/internal/gateway"
)

// SetUploadProcessor wires a Processor into the API instance. Called
// from main.go at startup once the agent and executor client exist.
// Done as a setter (rather than constructor argument) to keep the
// existing API.New signature stable.
func (a *API) SetUploadProcessor(p *uploads.Processor) { a.uploadProc = p }

/*
 * handleUploadFile receives a multipart upload and runs it through the
 * synchronous uploads.Processor pipeline: validate → write → extract
 * metadata → optional summary → memory entry. Returns the upload's
 * Result so the frontend can render the chip with size, lines, and
 * (for tiny files) inline content.
 */
func (a *API) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	_ = claims // currently unused but kept for parity + future per-user quota
	if a.uploadProc == nil {
		jsonError(w, "upload processor not initialised", http.StatusServiceUnavailable)
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		jsonError(w, "session id required", http.StatusBadRequest)
		return
	}

	// Bound the multipart parser at MaxFileSize; bigger requests get
	// rejected before consuming RAM.
	if err := r.ParseMultipartForm(uploads.MaxFileSize + 1024); err != nil {
		jsonError(w, "parse multipart: "+err.Error(), http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		jsonError(w, "missing 'file' field: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	res, err := a.uploadProc.Process(r.Context(), sid, header.Filename, header.Size, file)
	if err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, res, http.StatusOK)
}

/*
 * handleListUploads returns the upload Results for a session, by
 * scanning the .meta.json sidecars in uploads/<sid>/. Used to restore
 * the chip strip on session reload.
 */
func (a *API) handleListUploads(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	_ = claims
	if a.uploadProc == nil {
		jsonResponse(w, []any{}, http.StatusOK)
		return
	}
	sid := r.PathValue("id")
	if sid == "" {
		jsonError(w, "session id required", http.StatusBadRequest)
		return
	}
	list, err := a.uploadProc.List(sid)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, list, http.StatusOK)
}

/*
 * handleDeleteUpload removes a single upload (file + sidecars) and its
 * memory entry from a session. Path: DELETE /api/v1/sessions/{id}/uploads/{name}.
 */
func (a *API) handleDeleteUpload(w http.ResponseWriter, r *http.Request) {
	claims, ok := gateway.ClaimsFromContext(r.Context())
	if !ok {
		jsonError(w, "authentication required", http.StatusUnauthorized)
		return
	}
	_ = claims
	if a.uploadProc == nil {
		jsonError(w, "upload processor not initialised", http.StatusServiceUnavailable)
		return
	}
	sid := r.PathValue("id")
	name := r.PathValue("name")
	if sid == "" || name == "" {
		jsonError(w, "session id and filename required", http.StatusBadRequest)
		return
	}
	if err := a.uploadProc.Delete(sid, name); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResponse(w, map[string]string{"status": "deleted"}, http.StatusOK)
}
