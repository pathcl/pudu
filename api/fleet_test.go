package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pathcl/pudu/api"
	"github.com/pathcl/pudu/vm"
)

func newTestServer(t *testing.T) *api.Server {
	t.Helper()
	return api.NewServer(vm.Config{
		KernelImagePath: "/dev/null",
		RootFSPath:      "/dev/null",
		VCPUs:           1,
		MemSizeMiB:      128,
	})
}

func TestListFleets_Empty(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleets", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got []interface{}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want empty list, got %v", got)
	}
}

func TestGetFleet_NotFound(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/fleets/does-not-exist", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestDeleteFleet_NotFound(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/fleets/ghost", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestFleets_MethodNotAllowed(t *testing.T) {
	s := newTestServer(t)
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/v1/fleets", strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/v1/fleets: status = %d, want 405", method, w.Code)
		}
	}
}

func TestCreateFleet_InvalidJSON(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleets", strings.NewReader(`not json`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestCreateFleet_ReturnsUniqueIDs(t *testing.T) {
	s := newTestServer(t)

	create := func() api.FleetEntry {
		t.Helper()
		body := `{"count":3}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/fleets", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201", w.Code)
		}
		var entry api.FleetEntry
		if err := json.NewDecoder(w.Body).Decode(&entry); err != nil {
			t.Fatal(err)
		}
		return entry
	}

	first := create()
	second := create()

	// Collect all VM IDs
	seen := make(map[int]bool)
	for _, vmst := range first.VMs {
		seen[vmst.ID] = true
	}
	for _, vmst := range second.VMs {
		if seen[vmst.ID] {
			t.Errorf("VM ID %d appears in both fleets — IDs must be globally unique", vmst.ID)
		}
	}
}

func TestCreateFleet_FleetIDIsUUID(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleets", strings.NewReader(`{"count":1}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	var entry api.FleetEntry
	json.NewDecoder(w.Body).Decode(&entry) //nolint:errcheck
	if len(entry.ID) != 36 {
		t.Errorf("fleet ID %q does not look like a UUID", entry.ID)
	}
}

func TestDeleteFleet_ReleasesIDs(t *testing.T) {
	s := newTestServer(t)

	// Create fleet of 3 → IDs [0,1,2]
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleets", strings.NewReader(`{"count":3}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	var first api.FleetEntry
	json.NewDecoder(w.Body).Decode(&first) //nolint:errcheck

	// Delete it
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/fleets/"+first.ID, nil)
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", w.Code)
	}

	// Create another fleet of 3 → should reuse [0,1,2]
	req = httptest.NewRequest(http.MethodPost, "/api/v1/fleets", strings.NewReader(`{"count":3}`))
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	var second api.FleetEntry
	json.NewDecoder(w.Body).Decode(&second) //nolint:errcheck

	for i, vmst := range second.VMs {
		if vmst.ID != first.VMs[i].ID {
			t.Errorf("after delete+recreate: VM[%d].ID = %d, want %d (reused)", i, vmst.ID, first.VMs[i].ID)
		}
	}
}
