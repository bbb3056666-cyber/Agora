package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

type PprofServer struct {
	name            string
	server          *http.Server
	shutdownTimeout time.Duration
}

// NewPprofMux starts with no database metrics so existing callers can still use
// it as a plain pprof mux. When a *sql.DB is provided, it also exposes the
// process-local connection-pool snapshot on the debug port.
func NewPprofMux(dbs ...*sql.DB) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	if len(dbs) > 0 && dbs[0] != nil {
		sqlDB := dbs[0]
		mux.HandleFunc("/debug/db/stats", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			// sql.DB.Stats reports cumulative wait data since this API process started.
			// Compare two snapshots around one pressure run to obtain that run's delta.
			stats := sqlDB.Stats()
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(struct {
				MaxOpenConnections int   `json:"max_open_connections"`
				OpenConnections    int   `json:"open_connections"`
				InUse              int   `json:"in_use"`
				Idle               int   `json:"idle"`
				WaitCount          int64 `json:"wait_count"`
				WaitDurationMS     int64 `json:"wait_duration_ms"`
			}{
				MaxOpenConnections: stats.MaxOpenConnections,
				OpenConnections:    stats.OpenConnections,
				InUse:              stats.InUse,
				Idle:               stats.Idle,
				WaitCount:          stats.WaitCount,
				WaitDurationMS:     stats.WaitDuration.Milliseconds(),
			})
		})
	}

	return mux
}

func NewPprofServer(name string, enabled bool, addr string, dbs ...*sql.DB) (*PprofServer, error) {
	if !enabled || addr == "" {
		return nil, nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to start %s pprof server on %s: %w", name, addr, err)
	}
	pprofServer := &PprofServer{
		name:            name,
		shutdownTimeout: 3 * time.Second,
	}
	pprofServer.server = &http.Server{
		Addr:              addr,
		Handler:           NewPprofMux(dbs...),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("%s pprof listening on %s", name, addr)
		if err := pprofServer.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("%s pprof server error: %v", name, err)
		}
	}()
	return pprofServer, nil
}

func Shutdown(ctx context.Context, srv *http.Server) error {
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *PprofServer) Close() error {
	if s == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
	defer cancel()
	if err := Shutdown(shutdownCtx, s.server); err != nil {
		log.Printf("Failed to shutdown %s pprof server: %v", s.name, err)
		return err
	}
	return nil
}
