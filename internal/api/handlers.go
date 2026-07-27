package api

import (
	"encoding/json"
	"net/http"

	"github.com/OrcaxNet/vibe-forge/internal/store"
)

// createProject: POST /api/projects
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	var req struct {
		Title          string `json:"title"`
		InitialPrompt  string `json:"initialPrompt"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeContractError(w, "VALIDATION_ERROR", err.Error())
		return
	}
	key := idempotencyKey(r, req.IdempotencyKey)
	if !requireKey(w, key) {
		return
	}
	status, body, _, err := st.CreateProject(r.Context(), req.Title, req.InitialPrompt, key)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeRaw(w, status, body)
}

// listProjects: GET /api/projects
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	ps, err := st.ListProjects(r.Context())
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if ps == nil {
		ps = []store.Project{}
	}
	writeJSON(w, http.StatusOK, ps)
}

// getProject: GET /api/projects/{id}
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	d, err := st.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// createRun: POST /api/projects/{id}/runs
func (s *Server) createRun(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	// PRD-C / Stage 1 contract: run creation is rejected until the model is
	// configured (the agent loop cannot progress without it).
	if !s.modelConfigured() {
		writeContractError(w, "DEPENDENCY_UNAVAILABLE", "model is not configured")
		return
	}
	var req struct {
		Prompt         string `json:"prompt"`
		BaseVersionID  string `json:"baseVersionId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeContractError(w, "VALIDATION_ERROR", err.Error())
		return
	}
	key := idempotencyKey(r, req.IdempotencyKey)
	if !requireKey(w, key) {
		return
	}
	status, body, replayed, err := st.CreateRun(r.Context(), r.PathValue("id"), req.Prompt, req.BaseVersionID, key)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	// Launch the agent loop for the new run. Only on a genuine create (not an
	// idempotent replay) and only when the loop is wired (model configured). The
	// loop runs on the server-lifetime loopCtx, decoupled from this request.
	if !replayed && s.loop != nil {
		var created struct {
			RunID string `json:"runId"`
		}
		if json.Unmarshal(body, &created) == nil && created.RunID != "" {
			go s.loop.Run(s.loopCtx, created.RunID)
		}
	}
	writeRaw(w, status, body)
}

// writeFileIllegalPath: PUT /api/projects/{id}/files/{path...} for any path other
// than /src/App.tsx. Only /src/App.tsx is writable (contract
// §limits.writableFilePath, B-FR-01); every other path returns 422
// VALIDATION_ERROR (acceptance 3: illegal file path returns 422).
func (s *Server) writeFileIllegalPath(w http.ResponseWriter, r *http.Request) {
	writeContractError(w, "VALIDATION_ERROR",
		"only /src/App.tsx is writable; path "+r.PathValue("path")+" is read-only or invalid")
}

// listFiles: GET /api/projects/{id}/files
func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	tree, err := st.ListFiles(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tree)
}

// writeFile: PUT /api/projects/{id}/files/src/App.tsx (manual iteration)
func (s *Server) writeFile(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	var req struct {
		Content        string `json:"content"`
		BaseVersionID  string `json:"baseVersionId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeContractError(w, "VALIDATION_ERROR", err.Error())
		return
	}
	key := idempotencyKey(r, req.IdempotencyKey)
	if !requireKey(w, key) {
		return
	}
	status, body, _, err := st.WriteFile(r.Context(), r.PathValue("id"), req.Content, req.BaseVersionID, key)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeRaw(w, status, body)
}

// listVersions: GET /api/projects/{id}/versions
func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	vs, err := st.ListVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if vs == nil {
		vs = []store.Version{}
	}
	writeJSON(w, http.StatusOK, vs)
}

// versionFiles: GET /api/projects/{id}/versions/{versionId}/files
func (s *Server) versionFiles(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	files, err := st.GetVersionFiles(r.Context(), r.PathValue("id"), r.PathValue("versionId"))
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	if files == nil {
		files = map[string]string{}
	}
	writeJSON(w, http.StatusOK, files)
}

// restoreVersion: POST /api/projects/{id}/versions/{versionId}/restore
func (s *Server) restoreVersion(w http.ResponseWriter, r *http.Request) {
	st := s.requireStore(w)
	if st == nil {
		return
	}
	var req struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	// restoreVersion only needs the idempotency key; an empty body is allowed.
	_ = decodeJSON(r, &req)
	key := idempotencyKey(r, req.IdempotencyKey)
	if !requireKey(w, key) {
		return
	}
	status, body, _, err := st.RestoreVersion(r.Context(), r.PathValue("id"), r.PathValue("versionId"), key)
	if err != nil {
		s.writeStoreErr(w, err)
		return
	}
	writeRaw(w, status, body)
}
