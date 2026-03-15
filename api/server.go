package api

import (
	"net/http"
	"sync"

	"github.com/pathcl/pudu/vm"
)

// Server holds all in-memory state for the REST API.
type Server struct {
	mu        sync.RWMutex
	fleets    map[string]*FleetEntry
	scenarios map[string]*ScenarioEntry
	baseCfg   vm.Config
	usedIDs   map[int]bool // globally tracks which VM IDs are in use
}

// NewServer creates an API server with the given base VM configuration.
func NewServer(baseCfg vm.Config) *Server {
	return &Server{
		fleets:    make(map[string]*FleetEntry),
		scenarios: make(map[string]*ScenarioEntry),
		baseCfg:   baseCfg,
		usedIDs:   make(map[int]bool),
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
	mux.HandleFunc("/api/v1/openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(OpenAPISpec) //nolint:errcheck
	})
	return mux
}
