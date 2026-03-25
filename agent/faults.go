package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FaultRequest is the JSON body for POST /fault/start.
type FaultRequest struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

type activeFault struct {
	cancel context.CancelFunc
}

var (
	activeMu     sync.Mutex
	activeFaults = make(map[string]*activeFault)
)

// startFault dispatches to the appropriate fault implementation.
func startFault(req FaultRequest) error {
	ctx, cancel := context.WithCancel(context.Background())

	activeMu.Lock()
	if _, exists := activeFaults[req.ID]; exists {
		activeMu.Unlock()
		cancel()
		return fmt.Errorf("fault %q already running", req.ID)
	}
	activeFaults[req.ID] = &activeFault{cancel: cancel}
	activeMu.Unlock()

	var err error
	switch req.Type {
	case "cpu":
		go runCPUFault(ctx, req.Params)
	case "memory":
		go runMemoryFault(ctx, req.Params)
	case "disk", "filesystem":
		go runDiskFault(ctx, req.Params)
	case "network":
		err = runNetworkFault(ctx, req.Params)
	case "process":
		err = runProcessFault(req.Params)
	case "dns":
		err = runDNSFault(req.Params)
	default:
		cancel()
		activeMu.Lock()
		delete(activeFaults, req.ID)
		activeMu.Unlock()
		return fmt.Errorf("unknown fault type %q", req.Type)
	}

	if err != nil {
		cancel()
		activeMu.Lock()
		delete(activeFaults, req.ID)
		activeMu.Unlock()
		return err
	}

	// Apply duration-based auto-stop if specified
	if d, ok := req.Params["duration"]; ok {
		dur, err := time.ParseDuration(d)
		if err == nil {
			go func() {
				select {
				case <-time.After(dur):
					stopFault(req.ID)
				case <-ctx.Done():
				}
			}()
		}
	}

	return nil
}

// stopFault cancels and cleans up a running fault.
func stopFault(id string) {
	activeMu.Lock()
	f, ok := activeFaults[id]
	if ok {
		f.cancel()
		delete(activeFaults, id)
	}
	activeMu.Unlock()
}

// ── CPU fault ─────────────────────────────────────────────────────────────────

func runCPUFault(ctx context.Context, params map[string]string) {
	loadPct := 80
	if v, ok := params["load"]; ok {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "%")); err == nil {
			loadPct = n
		}
	}
	// Try stress-ng first
	if _, err := exec.LookPath("stress-ng"); err == nil {
		args := []string{"--cpu", strconv.Itoa(runtime.NumCPU()),
			"--cpu-load", strconv.Itoa(loadPct), "--timeout", "0"}
		cmd := exec.CommandContext(ctx, "stress-ng", args...)
		cmd.Run() //nolint:errcheck
		return
	}
	// Pure-Go fallback: busy goroutines with sleep gaps to hit target load
	numWorkers := runtime.NumCPU()
	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					// Work for loadPct ms, sleep for (100-loadPct) ms
					deadline := time.Now().Add(time.Duration(loadPct) * time.Millisecond)
					for time.Now().Before(deadline) {
						_ = rand.Float64() * rand.Float64()
					}
					time.Sleep(time.Duration(100-loadPct) * time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
}

// ── Memory fault ──────────────────────────────────────────────────────────────

func runMemoryFault(ctx context.Context, params map[string]string) {
	// rate: MB/min to allocate (simulates a leak)
	rateMBPerMin := 10
	if v, ok := params["rate"]; ok {
		// parse "50mb/min" or "50"
		v = strings.ToLower(strings.ReplaceAll(v, " ", ""))
		v = strings.TrimSuffix(v, "mb/min")
		v = strings.TrimSuffix(v, "mb")
		if n, err := strconv.Atoi(v); err == nil {
			rateMBPerMin = n
		}
	}
	ceilingPct := 90
	if v, ok := params["ceiling"]; ok {
		if n, err := strconv.Atoi(strings.TrimSuffix(v, "%")); err == nil {
			ceilingPct = n
		}
	}

	interval := time.Minute / time.Duration(rateMBPerMin)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var chunks [][]byte
	for {
		select {
		case <-ctx.Done():
			chunks = nil
			return
		case <-ticker.C:
			// Check current memory before allocating more
			_, avail, err := readMemInfo()
			if err == nil {
				total, _, _ := readMemInfo()
				if total > 0 {
					usedPct := float64(total-avail) / float64(total) * 100
					if usedPct >= float64(ceilingPct) {
						continue // don't allocate more
					}
				}
			}
			chunk := make([]byte, 1024*1024) // 1 MB
			for i := range chunk {
				chunk[i] = byte(i) // prevent GC optimisation
			}
			chunks = append(chunks, chunk)
		}
	}
}

// ── Disk fault ────────────────────────────────────────────────────────────────

func runDiskFault(ctx context.Context, params map[string]string) {
	path := "/"
	if v, ok := params["path"]; ok {
		path = v
	}
	fillPath := filepath.Join(path, ".onfire-diskfill")

	f, err := os.Create(fillPath)
	if err != nil {
		return
	}
	defer func() {
		f.Close()
		os.Remove(fillPath)
	}()

	buf := make([]byte, 1024*1024) // 1 MB chunks
	for i := range buf {
		buf[i] = 0xFF
	}

	fileGone := func() bool {
		_, err := os.Stat(fillPath)
		return os.IsNotExist(err)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
			// If the trainee deleted the file, close our fd to free the blocks.
			if fileGone() {
				return
			}
			if _, err := f.Write(buf); err != nil {
				// Disk full — poll until context cancelled or trainee removes the file.
				for {
					select {
					case <-ctx.Done():
						return
					case <-time.After(500 * time.Millisecond):
						if fileGone() {
							return
						}
					}
				}
			}
		}
	}
}

// ── Network fault ─────────────────────────────────────────────────────────────

func runNetworkFault(ctx context.Context, params map[string]string) error {
	iface := "eth0"
	if v, ok := params["interface"]; ok {
		iface = v
	}

	var tcArgs []string
	switch params["action"] {
	case "delay":
		latency := "100ms"
		if v, ok := params["latency"]; ok {
			latency = v
		}
		jitter := "10ms"
		if v, ok := params["jitter"]; ok {
			jitter = v
		}
		tcArgs = []string{"qdisc", "add", "dev", iface, "root", "netem",
			"delay", latency, jitter}
	case "loss", "drop":
		loss := "10%"
		if v, ok := params["packet_loss"]; ok {
			loss = v
		}
		tcArgs = []string{"qdisc", "add", "dev", iface, "root", "netem", "loss", loss}
	case "corrupt":
		tcArgs = []string{"qdisc", "add", "dev", iface, "root", "netem", "corrupt", "10%"}
	default:
		// Default: 200ms delay
		tcArgs = []string{"qdisc", "add", "dev", iface, "root", "netem", "delay", "200ms"}
	}

	if err := exec.Command("tc", tcArgs...).Run(); err != nil {
		return fmt.Errorf("tc failed: %w (is iproute2 installed?)", err)
	}

	// Clean up when context is cancelled
	go func() {
		<-ctx.Done()
		exec.Command("tc", "qdisc", "del", "dev", iface, "root").Run() //nolint:errcheck
	}()

	return nil
}

// ── Process fault ─────────────────────────────────────────────────────────────

func runProcessFault(params map[string]string) error {
	service := params["service"]
	if service == "" {
		return fmt.Errorf("process fault: params.service is required")
	}

	action := params["action"]
	if action == "" {
		action = "stop"
	}

	// Allow restart to be re-enabled or not
	restart := params["restart"] != "false"

	switch action {
	case "stop", "kill":
		if err := exec.Command("systemctl", "stop", service).Run(); err != nil {
			return fmt.Errorf("systemctl stop %s: %w", service, err)
		}
		if !restart {
			// Mask prevents auto-restart
			exec.Command("systemctl", "mask", service).Run() //nolint:errcheck
		}
	case "restart":
		exec.Command("systemctl", "restart", service).Run() //nolint:errcheck
	case "degrade":
		// Stop then unmask (service can restart but is currently down)
		exec.Command("systemctl", "stop", service).Run() //nolint:errcheck
	}

	return nil
}

// ── DNS fault ─────────────────────────────────────────────────────────────────

func runDNSFault(params map[string]string) error {
	record := params["record"]
	resolveTo := params["resolve_to"]
	if record == "" || resolveTo == "" {
		return fmt.Errorf("dns fault: params.record and params.resolve_to are required")
	}

	// Append to /etc/hosts
	entry := fmt.Sprintf("\n%s %s # onfire-fault\n", resolveTo, record)
	f, err := os.OpenFile("/etc/hosts", os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("opening /etc/hosts: %w", err)
	}
	defer f.Close()
	_, err = f.WriteString(entry)
	return err
}
