// pudu-agent runs inside each VM and executes fault injections on behalf of
// the scenario runner on the host. It exposes a simple HTTP API on port 7777.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
)

const agentPort = 7777

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/metrics", handleMetrics)
	mux.HandleFunc("/services", handleServices)
	mux.HandleFunc("/fault/start", handleFaultStart)
	mux.HandleFunc("/fault/stop", handleFaultStop)

	addr := fmt.Sprintf(":%d", agentPort)
	log.Printf("pudu-agent listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "agent error: %v\n", err)
		os.Exit(1)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true}) //nolint:errcheck
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
	m, err := collectMetrics()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m) //nolint:errcheck
}

func handleServices(w http.ResponseWriter, r *http.Request) {
	services := listServices()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(services) //nolint:errcheck
}

func handleFaultStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req FaultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := startFault(req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "status": "started"}) //nolint:errcheck
}

func handleFaultStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	stopFault(req.ID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": req.ID, "status": "stopped"}) //nolint:errcheck
}
