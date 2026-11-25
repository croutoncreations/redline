package api

import (
	"net/http"
	"time"

	"github.com/croutoncreations/redline/internal/config"
	"github.com/croutoncreations/redline/internal/cursorusage"
	"github.com/croutoncreations/redline/internal/nativeusage"
	"github.com/croutoncreations/redline/internal/openusage"
)

// newCursorTracker builds display-only Cursor tracking from configuration.
// Cursor's local session is read only when the operator chose the built-in
// collector (native or auto); the default only reads OpenUsage's loopback API.
func newCursorTracker(cfg config.Config, now func() time.Time) *cursorusage.Tracker {
	mode, explicit := cfg.CursorUsage.EffectiveSource()
	if mode == cursorusage.ModeOff {
		return nil
	}
	interval, err := cfg.CursorUsage.Interval()
	if err != nil {
		interval = 5 * time.Minute
	}
	var fromOpenUsage, native cursorusage.Fetcher
	if mode == cursorusage.ModeOpenUsage || mode == cursorusage.ModeAuto {
		fromOpenUsage = cursorusage.OpenUsageClient{Client: openusage.Client{BaseURL: cfg.CursorOpenUsageURL()}}
	}
	if mode == cursorusage.ModeNative || mode == cursorusage.ModeAuto {
		native = cursorusage.NativeClient{Credentials: nativeusage.NewCursorCredentials(), Now: now}
	}
	return cursorusage.NewTracker(mode, fromOpenUsage, native, interval, explicit, now)
}

// cursorUsageResponse is the GET /v1/cursor-usage body.
type cursorUsageResponse struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Cursor      *cursorusage.Report `json:"cursor"`
}

func (s *Server) cursorUsageSnapshot() *cursorusage.Report {
	report, ok := s.cursorUsage.Report()
	if !ok {
		return nil
	}
	return &report
}

func (s *Server) cursorUsageReport(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, cursorUsageResponse{GeneratedAt: s.now(), Cursor: s.cursorUsageSnapshot()})
}
