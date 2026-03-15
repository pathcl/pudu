package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pathcl/pudu/api"
	"github.com/pathcl/pudu/vm"
)

// ── Test helpers ──────────────────────────────────────────────────────────────

// noopDeps returns LaunchDeps where every system call is a no-op.
// Allows launchFleet goroutines to complete without root, Firecracker, or real files.
func noopDeps() api.LaunchDeps {
	return api.LaunchDeps{
		Factory: func(_ context.Context, _ vm.Config) (vm.VM, error) {
			return &fakeVM{}, nil
		},
		EnsureTAP:     func(id int) error { return nil },
		PrepareRootFS: func(base string, id int, _ int64) (string, error) { return base, nil },
		EnsureISO:     func(dst, src, hostname string) error { return nil },
	}
}

// recordingDeps returns LaunchDeps that records every VM ID launched and every
// cloud-init ISO path requested. Use the returned accessors to inspect calls.
func recordingDeps() (deps api.LaunchDeps, launchedIDs func() []int, isoPaths func() []string) {
	var mu sync.Mutex
	var ids []int
	var isos []string

	deps = api.LaunchDeps{
		Factory: func(_ context.Context, cfg vm.Config) (vm.VM, error) {
			// Extract ID from TapDeviceName "tapN"
			if n, _ := parseID(cfg.TapDeviceName, "tap"); n >= 0 {
				mu.Lock()
				ids = append(ids, n)
				mu.Unlock()
			}
			return &fakeVM{}, nil
		},
		EnsureTAP:     func(id int) error { return nil },
		PrepareRootFS: func(base string, id int, _ int64) (string, error) { return base, nil },
		EnsureISO: func(dst, src, hostname string) error {
			mu.Lock()
			isos = append(isos, dst)
			mu.Unlock()
			return nil
		},
	}
	launchedIDs = func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int{}, ids...)
	}
	isoPaths = func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, isos...)
	}
	return
}

func parseID(s, prefix string) (int, bool) {
	if !strings.HasPrefix(s, prefix) {
		return -1, false
	}
	var id int
	if _, err := fmt.Sscanf(s[len(prefix):], "%d", &id); err != nil {
		return -1, false
	}
	return id, true
}

// fakeVM satisfies vm.VM with no-op methods.
type fakeVM struct{}

func (f *fakeVM) Start(_ context.Context) error { return nil }
func (f *fakeVM) Wait(_ context.Context) error  { return nil }
func (f *fakeVM) Stop()                         {}

// newTestServer creates a Server with no-op deps so goroutines complete cleanly.
func newTestServer(t *testing.T) *api.Server {
	t.Helper()
	return api.NewServerWithDeps(vm.Config{
		KernelImagePath: "/dev/null",
		RootFSPath:      "/dev/null",
		VCPUs:           1,
		MemSizeMiB:      128,
	}, noopDeps())
}

// postFleet sends POST /api/v1/fleets and returns the decoded FleetEntry.
func postFleet(t *testing.T, s *api.Server, count int) api.FleetEntry {
	t.Helper()
	body := fmt.Sprintf(`{"count":%d}`, count)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fleets", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/fleets: status = %d, want 201\n%s", w.Code, w.Body.String())
	}
	var entry api.FleetEntry
	if err := json.NewDecoder(w.Body).Decode(&entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

// waitForStatus polls GET /api/v1/fleets/:id until the fleet reaches wantStatus
// or the timeout expires.
func waitForStatus(t *testing.T, s *api.Server, id, wantStatus string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/fleets/"+id, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		var entry api.FleetEntry
		json.NewDecoder(w.Body).Decode(&entry) //nolint:errcheck
		if entry.Status == wantStatus {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("fleet %s never reached status %q within %s", id, wantStatus, timeout)
}

// ── Basic HTTP handler tests ──────────────────────────────────────────────────

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

func TestCreateFleet_FleetIDIsUUID(t *testing.T) {
	s := newTestServer(t)
	entry := postFleet(t, s, 1)
	if len(entry.ID) != 36 {
		t.Errorf("fleet ID %q does not look like a UUID", entry.ID)
	}
}

// ── ID allocation tests ───────────────────────────────────────────────────────

// TestCreateFleet_VMIDsAreUnique is the regression test for the original bug:
// two fleets of 3 should get [0,1,2] and [3,4,5], never overlapping.
func TestCreateFleet_VMIDsAreUnique(t *testing.T) {
	s := newTestServer(t)

	first := postFleet(t, s, 3)
	second := postFleet(t, s, 3)

	seen := make(map[int]bool)
	for _, v := range first.VMs {
		seen[v.ID] = true
	}
	for _, v := range second.VMs {
		if seen[v.ID] {
			t.Errorf("VM ID %d appears in both fleets — must be globally unique", v.ID)
		}
	}
}

func TestDeleteFleet_ReleasesIDs(t *testing.T) {
	s := newTestServer(t)

	first := postFleet(t, s, 3) // IDs [0,1,2]

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/fleets/"+first.ID, nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", w.Code)
	}

	second := postFleet(t, s, 3) // should reuse [0,1,2]
	for i := range second.VMs {
		if second.VMs[i].ID != first.VMs[i].ID {
			t.Errorf("after release, VM[%d].ID = %d, want %d (reused)", i, second.VMs[i].ID, first.VMs[i].ID)
		}
	}
}

// ── Goroutine / launch path tests ─────────────────────────────────────────────

// TestLaunchFleet_UsesAllocatedIDs verifies that the goroutine actually launches
// VMs with the allocated IDs and not hardcoded 0..N-1.
// This is the test that would have caught the original ID collision bug.
func TestLaunchFleet_UsesAllocatedIDs(t *testing.T) {
	deps, getLaunched, _ := recordingDeps()
	s := api.NewServerWithDeps(vm.Config{
		KernelImagePath: "/dev/null",
		RootFSPath:      "/dev/null",
		VCPUs:           1,
		MemSizeMiB:      128,
	}, deps)

	first := postFleet(t, s, 3)
	second := postFleet(t, s, 3)

	// Wait for both fleets to complete (FakeVM.Wait returns immediately)
	waitForStatus(t, s, first.ID, "stopped", time.Second)
	waitForStatus(t, s, second.ID, "stopped", time.Second)

	launched := getLaunched()
	if len(launched) != 6 {
		t.Fatalf("expected 6 VMs launched, got %d: %v", len(launched), launched)
	}

	seen := make(map[int]bool)
	for _, id := range launched {
		if seen[id] {
			t.Errorf("VM ID %d launched twice — IDs must not overlap", id)
		}
		seen[id] = true
	}
}

// TestLaunchFleet_EnsuresCloudInitISO verifies that EnsureISO is called for each
// VM with the correct per-VM ISO path.
// This is the test that would have caught the missing cloud-init ISO bug.
func TestLaunchFleet_EnsuresCloudInitISO(t *testing.T) {
	deps, _, getISOs := recordingDeps()
	s := api.NewServerWithDeps(vm.Config{
		KernelImagePath: "/dev/null",
		RootFSPath:      "/dev/null",
		CloudInitISO:    "cloud-init.iso",
		VCPUs:           1,
		MemSizeMiB:      128,
	}, deps)

	first := postFleet(t, s, 3)
	second := postFleet(t, s, 3)

	waitForStatus(t, s, first.ID, "stopped", time.Second)
	waitForStatus(t, s, second.ID, "stopped", time.Second)

	isos := getISOs()
	if len(isos) != 6 {
		t.Fatalf("expected 6 EnsureISO calls (one per VM), got %d: %v", len(isos), isos)
	}

	want := map[string]bool{
		"cloud-init-0.iso": true, "cloud-init-1.iso": true, "cloud-init-2.iso": true,
		"cloud-init-3.iso": true, "cloud-init-4.iso": true, "cloud-init-5.iso": true,
	}
	for _, path := range isos {
		if !want[path] {
			t.Errorf("unexpected ISO path requested: %q", path)
		}
	}
}
