package observability

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewPprofMux(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
	rr := httptest.NewRecorder()

	NewPprofMux().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected status code 200, got %d", rr.Code)
	}
}

func TestNewPprofMuxWithDBStats(t *testing.T) {
	t.Parallel()

	// A zero-value sql.DB is sufficient here: Stats itself does not open a connection.
	pool := &sql.DB{}
	req := httptest.NewRequest(http.MethodGet, "/debug/db/stats", nil)
	rr := httptest.NewRecorder()

	NewPprofMux(pool).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("Expected status code 200, got %d", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("Expected JSON response, got error: %v", err)
	}
	if _, ok := got["wait_count"]; !ok {
		t.Fatal("Expected wait_count in database stats response")
	}
}
func TestNewPprofServerWithDisabled(t *testing.T) {
	t.Parallel()

	pprofServer, err := NewPprofServer("api", false, "localhost:6060")
	if err != nil {
		t.Fatalf("Failed to create pprof server: %v", err)
	}
	if pprofServer != nil {
		t.Fatalf("Expected nil pprof server when disabled, got non-nil")
	}
}

func TestPprofServerCloseWithDisabledServer(t *testing.T) {
	t.Parallel()

	pprofServer, err := NewPprofServer("api", false, "localhost:6060")
	if err != nil {
		t.Fatalf("Failed to create pprof server: %v", err)
	}
	if err := pprofServer.Close(); err != nil {
		t.Fatalf("Expected no error when closing disabled pprof server, got: %v", err)
	}
}
