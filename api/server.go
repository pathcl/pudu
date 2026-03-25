package api

import (
	"net/http"
	"sync"

	"github.com/pathcl/onfire/vm"
)

// LaunchDeps holds all side-effectful dependencies of launchFleet.
// Production code uses DefaultLaunchDeps(); tests substitute no-ops.
type LaunchDeps struct {
	Factory       vm.Factory
	EnsureTAP     func(id int) error
	RemoveTAP     func(id int)
	PrepareRootFS func(base string, id int, sizeMiB int64) (string, error)
	EnsureISO     func(dst, src, hostname string) error
}

// DefaultLaunchDeps returns the real implementations for production use.
func DefaultLaunchDeps() LaunchDeps {
	return LaunchDeps{
		Factory:       vm.New,
		EnsureTAP:     vm.EnsureTAP,
		RemoveTAP:     vm.RemoveTAP,
		PrepareRootFS: vm.PrepareRootFS,
		EnsureISO:     vm.EnsureCloudInitISO,
	}
}

// Server holds all in-memory state for the REST API.
type Server struct {
	mu        sync.RWMutex
	fleets    map[string]*FleetEntry
	scenarios map[string]*ScenarioEntry
	baseCfg   vm.Config
	usedIDs   map[int]bool // globally tracks which VM IDs are in use
	deps      LaunchDeps
}

// NewServer creates an API server with the given base VM configuration.
func NewServer(baseCfg vm.Config) *Server {
	return NewServerWithDeps(baseCfg, DefaultLaunchDeps())
}

// NewServerWithDeps creates a Server with explicit launch dependencies.
// Use this in tests to inject FakeVM and no-op system calls.
func NewServerWithDeps(baseCfg vm.Config, deps LaunchDeps) *Server {
	return &Server{
		fleets:    make(map[string]*FleetEntry),
		scenarios: make(map[string]*ScenarioEntry),
		baseCfg:   baseCfg,
		usedIDs:   make(map[int]bool),
		deps:      deps,
	}
}

// allocateVMIDs picks count available VM IDs (lowest first) and marks them used.
// Must be called with s.mu write-locked.
func (s *Server) allocateVMIDs(count int) []int {
	ids := make([]int, 0, count)
	candidate := 0
	for len(ids) < count {
		if !s.usedIDs[candidate] {
			ids = append(ids, candidate)
		}
		candidate++
	}
	for _, id := range ids {
		s.usedIDs[id] = true
	}
	return ids
}

// releaseVMIDs marks the given IDs as available for reuse.
// Must be called with s.mu write-locked.
func (s *Server) releaseVMIDs(ids []int) {
	for _, id := range ids {
		delete(s.usedIDs, id)
	}
}

// TotalVMs returns the total number of VMs across all running fleets.
func (s *Server) TotalVMs() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	total := 0
	for _, f := range s.fleets {
		if f.Status == "running" || f.Status == "starting" {
			total += f.Count
		}
	}
	return total
}

// Handler returns an http.Handler with all API routes mounted.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/fleets", s.handleFleets)
	mux.HandleFunc("/api/v1/fleets/", s.handleFleet)
	mux.HandleFunc("/api/v1/scenarios", s.handleScenarios)
	mux.HandleFunc("/api/v1/scenarios/", s.handleScenario)
	mux.HandleFunc("/api/v1/vms/", s.handleVM)
	mux.HandleFunc("/api/v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(OpenAPISpec) //nolint:errcheck
	})
	return mux
}
