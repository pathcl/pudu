package api

import (
	"testing"

	"github.com/pathcl/pudu/vm"
)

func newTestServer() *Server {
	return NewServer(vm.Config{
		KernelImagePath: "/dev/null",
		RootFSPath:      "/dev/null",
		VCPUs:           1,
		MemSizeMiB:      128,
	})
}

// TestAllocateVMIDs_NoOverlap verifies that two consecutive allocations
// return disjoint ID sets — the root cause of the "always 0,1,2" bug.
func TestAllocateVMIDs_NoOverlap(t *testing.T) {
	s := newTestServer()

	first := s.allocateVMIDs(3)
	second := s.allocateVMIDs(3)

	seen := make(map[int]bool)
	for _, id := range first {
		seen[id] = true
	}
	for _, id := range second {
		if seen[id] {
			t.Errorf("ID %d allocated twice: first=%v second=%v", id, first, second)
		}
	}
}

// TestAllocateVMIDs_ReuseAfterRelease verifies released IDs become available again.
func TestAllocateVMIDs_ReuseAfterRelease(t *testing.T) {
	s := newTestServer()

	first := s.allocateVMIDs(3) // [0,1,2]
	s.releaseVMIDs(first)

	second := s.allocateVMIDs(3) // should get [0,1,2] again

	for i, id := range second {
		if id != first[i] {
			t.Errorf("after release, expected ID %d at position %d, got %d", first[i], i, id)
		}
	}
}

// TestAllocateVMIDs_GapFilling verifies that gaps left by partial releases are filled first.
func TestAllocateVMIDs_GapFilling(t *testing.T) {
	s := newTestServer()

	s.allocateVMIDs(3)         // [0,1,2]
	s.releaseVMIDs([]int{1})   // release only 1

	next := s.allocateVMIDs(1) // should get [1] (lowest available)
	if next[0] != 1 {
		t.Errorf("expected gap ID 1 to be reused, got %d", next[0])
	}
}

// TestTotalVMs_CountsOnlyRunning verifies stopped fleets are excluded.
func TestTotalVMs_CountsOnlyRunning(t *testing.T) {
	s := newTestServer()

	s.mu.Lock()
	s.fleets["a"] = &FleetEntry{ID: "a", Count: 2, Status: "running"}
	s.fleets["b"] = &FleetEntry{ID: "b", Count: 3, Status: "stopped"}
	s.fleets["c"] = &FleetEntry{ID: "c", Count: 1, Status: "starting"}
	s.mu.Unlock()

	if got := s.TotalVMs(); got != 3 {
		t.Errorf("TotalVMs() = %d, want 3 (running + starting only)", got)
	}
}
