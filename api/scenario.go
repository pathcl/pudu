package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pathcl/onfire/scenario"
)

// ScenarioEntry tracks a running scenario.
type ScenarioEntry struct {
	ID        string     `json:"id"`
	Title     string     `json:"title"`
	File      string     `json:"file"`
	Status    string     `json:"status"` // running|complete|aborted
	Score     int        `json:"score"`
	Elapsed   string     `json:"elapsed"`
	HintsUsed int        `json:"hints_used"`
	VMs       []VMStatus `json:"vms"`
	CreatedAt time.Time  `json:"created_at"`
	runner    *scenario.Runner
	cancel    context.CancelFunc
	startAt   time.Time
}

// scenarioCreateRequest is the JSON body for POST /api/v1/scenarios.
type scenarioCreateRequest struct {
	ScenarioFile   string `json:"scenario_file"`
	Kernel         string `json:"kernel"`
	RootFS         string `json:"rootfs"`
	CloudInitISO   string `json:"cloud_init_iso"`
	FirecrackerBin string `json:"firecracker_bin"`
	Scale          string `json:"scale"` // e.g. "web=2,db=1"
	WebPort        int    `json:"web_port"`
}

// hintResponse is returned by POST /api/v1/scenarios/:id/hint.
type hintResponse struct {
	Hint  string `json:"hint"`
	Score int    `json:"score"`
}

func (s *Server) handleScenarios(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createScenario(w, r)
	case http.MethodGet:
		s.listScenarios(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleScenario(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/scenarios/")
	parts := strings.SplitN(path, "/", 2)
	id := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	if id == "" {
		http.Error(w, "missing scenario id", http.StatusBadRequest)
		return
	}

	switch {
	case r.Method == http.MethodGet && sub == "":
		s.getScenario(w, r, id)
	case r.Method == http.MethodPost && sub == "hint":
		s.hintScenario(w, r, id)
	case r.Method == http.MethodDelete && sub == "":
		s.deleteScenario(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createScenario(w http.ResponseWriter, r *http.Request) {
	var req scenarioCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ScenarioFile == "" {
		http.Error(w, "scenario_file is required", http.StatusBadRequest)
		return
	}

	sc, err := scenario.Load(req.ScenarioFile)
	if err != nil {
		http.Error(w, "failed to load scenario: "+err.Error(), http.StatusBadRequest)
		return
	}

	cfg := s.baseCfg
	if req.Kernel != "" {
		cfg.KernelImagePath = req.Kernel
	}
	if req.RootFS != "" {
		cfg.RootFSPath = req.RootFS
	}
	if req.CloudInitISO != "" {
		cfg.CloudInitISO = req.CloudInitISO
	}
	if req.FirecrackerBin != "" {
		cfg.FirecrackerBin = req.FirecrackerBin
	}

	webPort := req.WebPort
	if webPort == 0 {
		webPort = 8888
	}

	scaleOverrides := parseScaleOverrides(req.Scale)

	// Pre-build the plan so we know how many VMs to allocate IDs for.
	tmpPlan, err := scenario.BuildVMPlan(sc, scaleOverrides, nil)
	if err != nil {
		http.Error(w, "failed to build VM plan: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	vmIDs := s.allocateVMIDs(tmpPlan.TotalVMs)
	s.mu.Unlock()

	opts := scenario.RunOptions{
		KernelPath:     cfg.KernelImagePath,
		RootFSPath:     cfg.RootFSPath,
		CloudInitISO:   cfg.CloudInitISO,
		FirecrackerBin: cfg.FirecrackerBin,
		ScaleOverrides: scaleOverrides,
		WebPort:        webPort,
		VMIDs:          vmIDs,
	}

	runner, err := scenario.NewRunner(sc, opts)
	if err != nil {
		s.mu.Lock()
		s.releaseVMIDs(vmIDs)
		s.mu.Unlock()
		http.Error(w, "failed to create runner: "+err.Error(), http.StatusInternalServerError)
		return
	}

	vms := make([]VMStatus, len(vmIDs))
	for i, vid := range vmIDs {
		vms[i] = VMStatus{ID: vid, IP: fmt.Sprintf("172.16.%d.2", vid)}
	}

	id := uuid.New().String()
	ctx, cancel := context.WithCancel(context.Background())
	entry := &ScenarioEntry{
		ID:        id,
		Title:     sc.Meta.Title,
		File:      req.ScenarioFile,
		Status:    "running",
		VMs:       vms,
		CreatedAt: time.Now(),
		startAt:   time.Now(),
		runner:    runner,
		cancel:    cancel,
	}

	s.mu.Lock()
	s.scenarios[id] = entry
	s.mu.Unlock()

	go func() {
		if err := runner.Run(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "scenario %s error: %v\n", id, err)
		}
		s.mu.Lock()
		if entry.Status == "running" {
			if ctx.Err() != nil {
				entry.Status = "aborted"
			} else {
				entry.Status = "complete"
			}
		}
		s.releaseVMIDs(vmIDs)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(entry) //nolint:errcheck
}

func (s *Server) listScenarios(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	list := make([]*ScenarioEntry, 0, len(s.scenarios))
	for _, sc := range s.scenarios {
		list = append(list, sc)
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list) //nolint:errcheck
}

func (s *Server) getScenario(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	entry, ok := s.scenarios[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}

	s.mu.Lock()
	if entry.runner != nil {
		entry.Score = entry.runner.CurrentScore()
		entry.Elapsed = time.Since(entry.startAt).Round(time.Second).String()
	}
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry) //nolint:errcheck
}

func (s *Server) hintScenario(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	entry, ok := s.scenarios[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}

	if entry.runner == nil {
		http.Error(w, "runner not available", http.StatusInternalServerError)
		return
	}

	hint := entry.runner.RequestHintText()
	score := entry.runner.CurrentScore()

	s.mu.Lock()
	entry.HintsUsed++
	entry.Score = score
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(hintResponse{Hint: hint, Score: score}) //nolint:errcheck
}

func (s *Server) deleteScenario(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	entry, ok := s.scenarios[id]
	if ok {
		entry.cancel()
		entry.Status = "aborted"
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleVM routes requests under /api/v1/vms/.
// Currently supports: POST /api/v1/vms/{id}/hint
func (s *Server) handleVM(w http.ResponseWriter, r *http.Request) {
	// Expect path: /api/v1/vms/{id}/hint
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/vms/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[1] != "hint" || r.Method != http.MethodPost {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	vmID, err := strconv.Atoi(parts[0])
	if err != nil {
		http.Error(w, "invalid vm id", http.StatusBadRequest)
		return
	}

	// Find the running scenario that owns this VM ID.
	s.mu.RLock()
	var entry *ScenarioEntry
	for _, sc := range s.scenarios {
		if sc.Status != "running" {
			continue
		}
		for _, vm := range sc.VMs {
			if vm.ID == vmID {
				entry = sc
				break
			}
		}
		if entry != nil {
			break
		}
	}
	s.mu.RUnlock()

	if entry == nil {
		http.Error(w, "no running scenario owns this VM", http.StatusNotFound)
		return
	}
	if entry.runner == nil {
		http.Error(w, "runner not available", http.StatusInternalServerError)
		return
	}

	hint := entry.runner.RequestHintText()
	score := entry.runner.CurrentScore()

	s.mu.Lock()
	entry.HintsUsed++
	entry.Score = score
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(hintResponse{Hint: hint, Score: score}) //nolint:errcheck
}

// parseScaleOverrides parses "web=2,db=1" into map[string]int.
func parseScaleOverrides(s string) map[string]int {
	overrides := make(map[string]int)
	if s == "" {
		return overrides
	}
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		var n int
		fmt.Sscanf(kv[1], "%d", &n)
		if n > 0 {
			overrides[strings.TrimSpace(kv[0])] = n
		}
	}
	return overrides
}
