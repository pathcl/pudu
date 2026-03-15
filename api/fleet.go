package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/pathcl/pudu/vm"
)

// VMStatus is the public view of a single VM in a fleet.
type VMStatus struct {
	ID int    `json:"id"`
	IP string `json:"ip"`
}

// FleetEntry tracks a running fleet.
type FleetEntry struct {
	ID        string     `json:"id"`
	Count     int        `json:"count"`
	Status    string     `json:"status"` // starting|running|stopped
	VMs       []VMStatus `json:"vms"`
	WebPort   int        `json:"web_port"`
	CreatedAt time.Time  `json:"created_at"`
	vmIDs     []int      // allocated IDs, released on delete
	cancel    context.CancelFunc
}

// fleetCreateRequest is the JSON body for POST /api/v1/fleets.
type fleetCreateRequest struct {
	Count        int    `json:"count"`
	Kernel       string `json:"kernel"`
	RootFS       string `json:"rootfs"`
	CloudInitISO string `json:"cloud_init_iso"`
	MemMB        int64  `json:"mem_mb"`
	VCPUs        int64  `json:"vcpus"`
}

func (s *Server) handleFleets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.createFleet(w, r)
	case http.MethodGet:
		s.listFleets(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/fleets/")
	if id == "" {
		http.Error(w, "missing fleet id", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.getFleet(w, r, id)
	case http.MethodDelete:
		s.deleteFleet(w, r, id)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) createFleet(w http.ResponseWriter, r *http.Request) {
	var req fleetCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Count < 1 {
		req.Count = 1
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
	if req.MemMB > 0 {
		cfg.MemSizeMiB = req.MemMB
	}
	if req.VCPUs > 0 {
		cfg.VCPUs = req.VCPUs
	}

	// Allocate non-overlapping VM IDs under the lock
	s.mu.Lock()
	vmIDs := s.allocateVMIDs(req.Count)

	vms := make([]VMStatus, len(vmIDs))
	for i, id := range vmIDs {
		vms[i] = VMStatus{ID: id, IP: fmt.Sprintf("172.16.%d.2", id)}
	}

	ctx, cancel := context.WithCancel(context.Background())
	entry := &FleetEntry{
		ID:        uuid.New().String(),
		Count:     req.Count,
		Status:    "starting",
		VMs:       vms,
		CreatedAt: time.Now(),
		vmIDs:     vmIDs,
		cancel:    cancel,
	}
	s.fleets[entry.ID] = entry
	s.mu.Unlock()

	go func() {
		s.mu.Lock()
		entry.Status = "running"
		s.mu.Unlock()

		if err := launchFleet(ctx, cfg, vmIDs, s.deps); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "fleet %s error: %v\n", entry.ID, err)
		}

		s.mu.Lock()
		entry.Status = "stopped"
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(entry) //nolint:errcheck
}

func (s *Server) listFleets(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	list := make([]*FleetEntry, 0, len(s.fleets))
	for _, f := range s.fleets {
		list = append(list, f)
	}
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list) //nolint:errcheck
}

func (s *Server) getFleet(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.RLock()
	entry, ok := s.fleets[id]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, "fleet not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entry) //nolint:errcheck
}

func (s *Server) deleteFleet(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	entry, ok := s.fleets[id]
	if ok {
		entry.cancel()
		entry.Status = "stopped"
		s.releaseVMIDs(entry.vmIDs)
	}
	s.mu.Unlock()

	if !ok {
		http.Error(w, "fleet not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// launchFleet launches VMs for the given vmIDs in parallel.
// All system calls are routed through deps, making the function testable
// without Firecracker, root privileges, or real filesystem images.
func launchFleet(ctx context.Context, baseCfg vm.Config, vmIDs []int, deps LaunchDeps) error {
	var wg sync.WaitGroup
	errChan := make(chan error, len(vmIDs))

	for _, id := range vmIDs {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			cfg := baseCfg.ForVM(id)

			if err := deps.EnsureTAP(id); err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: tap setup: %w", id, err)
				}
				return
			}

			// Generate per-VM cloud-init ISO on demand (may not exist for higher IDs)
			if baseCfg.CloudInitISO != "" {
				isoPath := cfg.CloudInitISO // already set to cloud-init-{id}.iso by ForVM
				hostname := fmt.Sprintf("vm-%d", id)
				if err := deps.EnsureISO(isoPath, "cloud-init-config.yaml", hostname); err != nil {
					if ctx.Err() == nil {
						errChan <- fmt.Errorf("VM %d: cloud-init ISO: %w", id, err)
					}
					return
				}
			}

			vmRootFS, err := deps.PrepareRootFS(baseCfg.RootFSPath, id, baseCfg.RootFSSizeMiB)
			if err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: prepare rootfs: %w", id, err)
				}
				return
			}
			defer os.Remove(vmRootFS)
			cfg.RootFSPath = vmRootFS

			m, err := deps.Factory(ctx, cfg)
			if err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: create: %w", id, err)
				}
				return
			}
			defer m.Stop()

			if err := m.Start(ctx); err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: start: %w", id, err)
				}
				return
			}

			if err := m.Wait(ctx); err != nil && ctx.Err() == nil {
				errChan <- fmt.Errorf("VM %d: exited: %w", id, err)
			}
		}(id)
	}

	go func() {
		wg.Wait()
		close(errChan)
	}()

	var lastErr error
	for err := range errChan {
		lastErr = err
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return lastErr
}
