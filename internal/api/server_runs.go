package api

import (
	"errors"
	"net/http"
	"os"
	"strconv"

	"github.com/jfox/redline/internal/artifacts"
	"github.com/jfox/redline/internal/workspace"
)

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.ListRuns(r.Context(), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listRunEvents(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := s.store.ListRunEvents(r.Context(), r.PathValue("run"), limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (s *Server) getRunLogs(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRun(r.Context(), r.PathValue("run"))
	if err != nil {
		writeError(w, err)
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "stdout"
	}
	var path string
	switch stream {
	case "stdout":
		path = run.OutputFile
	case "stderr":
		path = run.ErrorFile
	case "prepare_stdout":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "prepare", "stdout")
	case "prepare_stderr":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "prepare", "stderr")
	case "finalize_stdout":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "finalize", "stdout")
	case "finalize_stderr":
		path = workspace.ArtifactPath(s.config.ArtifactsDirectory(), run.ID, "finalize", "stderr")
	default:
		writeJSON(w, http.StatusBadRequest, problem{Error: "unsupported log stream"})
		return
	}
	if path == "" {
		writeJSON(w, http.StatusNotFound, problem{Error: stream + " artifact is not available"})
		return
	}
	tailBytes := int64(32 * 1024)
	if configured := r.URL.Query().Get("tail_bytes"); configured != "" {
		tailBytes, err = strconv.ParseInt(configured, 10, 64)
		if err != nil || tailBytes <= 0 {
			writeJSON(w, http.StatusBadRequest, problem{Error: "tail_bytes must be a positive integer"})
			return
		}
	}
	tail, err := s.artifacts.ReadTail(path, tailBytes)
	if errors.Is(err, artifacts.ErrOutsideRoot) {
		writeJSON(w, http.StatusForbidden, problem{Error: err.Error()})
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		if stream != "stdout" && stream != "stderr" {
			writeJSON(w, http.StatusOK, artifacts.Tail{})
			return
		}
		writeJSON(w, http.StatusNotFound, problem{Error: "artifact file does not exist"})
		return
	}
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tail)
}
