package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"os/exec"

	"github.com/pathcl/pudu/vm"
	"golang.org/x/crypto/ssh"
)

// WebTerminalFunc is called after VMs boot to start an interactive terminal.
// vmCount is the number of VMs; the function should block until ctx is done.
type WebTerminalFunc func(ctx context.Context, vmCount, port int)

// Runner orchestrates a scenario: launches VMs, injects faults, checks objectives.
type Runner struct {
	Scenario       *Scenario
	Plan           *VMPlan
	opts           RunOptions
	score          Score
	mu             sync.Mutex
	startAt        time.Time
	hintsUsed      int
	// WebTerminal, if set, is called after VMs boot to start the browser terminal.
	WebTerminal    WebTerminalFunc
}

// NewRunner creates a Runner from a loaded Scenario.
func NewRunner(s *Scenario, opts RunOptions) (*Runner, error) {
	plan, err := BuildVMPlan(s, opts.ScaleOverrides)
	if err != nil {
		return nil, err
	}
	r := &Runner{
		Scenario: s,
		Plan:     plan,
		opts:     opts,
		score: Score{
			Base:                 s.Scoring.Base,
			TimePenaltyPerSecond: s.Scoring.TimePenaltyPerSecond,
			HintPenalty:          s.Scoring.HintPenalty,
		},
	}
	return r, nil
}

// Run launches the fleet, injects faults per schedule, monitors objectives,
// and blocks until all objectives are met or ctx is cancelled.
func (r *Runner) Run(ctx context.Context) error {
	fmt.Fprintf(os.Stderr, "\n=== %s ===\n%s\n\n", r.Scenario.Meta.Title, r.Scenario.Meta.Description)
	fmt.Fprintf(os.Stderr, "Difficulty: %s | Architecture: %s\n\n", r.Scenario.Meta.Difficulty, r.Scenario.Meta.Architecture)

	if r.opts.DryRun {
		fmt.Fprintf(os.Stderr, "[dry-run] would launch %d VM(s)\n", r.Plan.TotalVMs)
		r.printSignals()
		return nil
	}

	// Build base VM config
	baseCfg := vm.DefaultConfig()
	baseCfg.KernelImagePath = r.opts.KernelPath
	baseCfg.RootFSPath = r.opts.RootFSPath
	baseCfg.CloudInitISO = r.opts.CloudInitISO
	if r.opts.FirecrackerBin != "" {
		baseCfg.FirecrackerBin = r.opts.FirecrackerBin
	}

	// Generate per-VM cloud-init ISOs (always fresh — no stat skip)
	if baseCfg.CloudInitISO != "" {
		base := baseCfg.CloudInitISO
		stem := base[:len(base)-4] // strip .iso
		for i := 0; i < r.Plan.TotalVMs; i++ {
			dst := fmt.Sprintf("%s-%d.iso", stem, i)
			hostname := r.Plan.VMs[i].Name
			cmd := exec.Command("bash", "make-cloud-init-iso.sh", dst, "cloud-init-config.yaml", hostname)
			if out, err := cmd.CombinedOutput(); err != nil {
				return fmt.Errorf("cloud-init ISO for VM %d: %w\n%s", i, err, out)
			}
		}
	}

	// Build MOTD once; it is injected into each VM's rootfs via debugfs below.
	motd := r.buildMOTD()

	// Launch all VMs
	fmt.Fprintf(os.Stderr, "Launching %d VM(s)...\n", r.Plan.TotalVMs)
	fleetCtx, cancelFleet := context.WithCancel(ctx)
	defer cancelFleet()

	var fleetWg sync.WaitGroup
	fleetErrs := make(chan error, r.Plan.TotalVMs)

	for _, entry := range r.Plan.VMs {
		fleetWg.Add(1)
		e := entry
		go func() {
			defer fleetWg.Done()
			cfg := baseCfg.ForVM(e.Index)
			if e.VCPUs > 0 {
				cfg.VCPUs = e.VCPUs
			}
			if e.MemMB > 0 {
				cfg.MemSizeMiB = e.MemMB
			}
			if err := vm.EnsureTAP(e.Index); err != nil {
				fleetErrs <- fmt.Errorf("VM %s: tap setup: %w", e.Name, err)
				return
			}

			rootFS, err := vm.PrepareRootFS(baseCfg.RootFSPath, e.Index, baseCfg.RootFSSizeMiB)
			if err != nil {
				fleetErrs <- fmt.Errorf("VM %s: prepare rootfs: %w", e.Name, err)
				return
			}
			defer os.Remove(rootFS)
			cfg.RootFSPath = rootFS

			// Inject MOTD directly into the ext4 image via debugfs so it is
			// present before sshd accepts the first connection.
			if motd != "" {
				if err := vm.WriteToRootFS(rootFS, "/etc/motd", motd); err != nil {
					fmt.Fprintf(os.Stderr, "  warning: could not write MOTD to %s: %v\n", e.Name, err)
				}
			}

			m, err := vm.New(fleetCtx, cfg)
			if err != nil {
				fleetErrs <- fmt.Errorf("VM %s: create: %w", e.Name, err)
				return
			}
			defer m.Stop()

			if err := m.Start(fleetCtx); err != nil {
				fleetErrs <- fmt.Errorf("VM %s: start: %w", e.Name, err)
				return
			}
			fmt.Fprintf(os.Stderr, "  ✓ %s  ssh root@172.16.%d.2\n", e.Name, e.Index)

			m.Wait(fleetCtx) //nolint:errcheck
		}()
	}

	// Wait for VMs to boot and SSH to come up
	time.Sleep(8 * time.Second)

	// Provision tier services via SSH
	fmt.Fprintf(os.Stderr, "\nProvisioning VM services...\n")
	if err := r.provisionAll(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: provisioning incomplete: %v\n", err)
	}

	// Start web terminal if configured
	if r.WebTerminal != nil && r.opts.WebPort > 0 {
		go r.WebTerminal(ctx, r.Plan.TotalVMs, r.opts.WebPort)
		fmt.Fprintf(os.Stderr, "==> WebSSH terminal: http://localhost:%d\n", r.opts.WebPort)
	}

	r.startAt = time.Now()
	r.score.Elapsed = 0

	// Print the signals the trainee sees
	r.printSignals()

	// Schedule faults
	faultCancelFuncs := make(map[string]context.CancelFunc)
	var faultMu sync.Mutex

	for i := range r.Scenario.Faults {
		f := r.Scenario.Faults[i]
		if len(f.Triggers) > 0 {
			continue // triggered faults are handled by trigger evaluator
		}
		atDur, err := f.AtDuration()
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: fault %s invalid at=%q: %v\n", f.ID, f.At, err)
			continue
		}
		go func() {
			select {
			case <-time.After(atDur):
			case <-ctx.Done():
				return
			}
			if err := r.injectFault(ctx, f); err != nil {
				fmt.Fprintf(os.Stderr, "fault %s: %v\n", f.ID, err)
			} else {
				faultMu.Lock()
				faultCancelFuncs[f.ID] = func() { r.stopFault(ctx, f) }
				faultMu.Unlock()
			}
		}()
	}

	// Poll objectives until all pass
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.mu.Lock()
				r.score.Elapsed = time.Since(r.startAt)
				r.mu.Unlock()

				if r.checkAllObjectives(ctx) {
					close(done)
					return
				}
			}
		}
	}()

	select {
	case <-done:
		r.mu.Lock()
		r.score.Elapsed = time.Since(r.startAt)
		r.score.HintsUsed = r.hintsUsed
		r.mu.Unlock()
		r.printResult(true)
		cancelFleet()
	case <-ctx.Done():
		r.mu.Lock()
		r.score.Elapsed = time.Since(r.startAt)
		r.mu.Unlock()
		r.printResult(false)
	}

	fleetWg.Wait()
	return nil
}

// RequestHint prints the next unused hint (if any) and deducts points.
func (r *Runner) RequestHint() {
	r.mu.Lock()
	idx := r.hintsUsed
	r.hintsUsed++
	r.mu.Unlock()

	if idx >= len(r.Scenario.Hints) {
		fmt.Println("No more hints available.")
		return
	}
	fmt.Printf("Hint: %s\n", r.Scenario.Hints[idx].Text)
	fmt.Printf("(-%d points)\n", r.Scenario.Scoring.HintPenalty)
}

// RequestHintText returns the next unused hint text and deducts points.
// Returns an empty string if no hints remain.
func (r *Runner) RequestHintText() string {
	r.mu.Lock()
	idx := r.hintsUsed
	r.hintsUsed++
	r.mu.Unlock()

	if idx >= len(r.Scenario.Hints) {
		return ""
	}
	return r.Scenario.Hints[idx].Text
}

// CurrentScore returns the live score.
func (r *Runner) CurrentScore() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.score.Elapsed = time.Since(r.startAt)
	r.score.HintsUsed = r.hintsUsed
	return r.score.Current()
}

// ── Fault delivery ────────────────────────────────────────────────────────────

// AgentFaultRequest mirrors agent.FaultRequest for HTTP delivery.
type AgentFaultRequest struct {
	ID     string            `json:"id"`
	Type   string            `json:"type"`
	Params map[string]string `json:"params"`
}

func (r *Runner) injectFault(ctx context.Context, f Fault) error {
	targets, err := ResolveTargets(f.Target, r.Plan)
	if err != nil {
		return err
	}
	params := make(map[string]string)
	for k, v := range f.Params {
		params[k] = v
	}
	if f.Duration != "" {
		params["duration"] = f.Duration
	}

	req := AgentFaultRequest{ID: f.ID, Type: string(f.Type), Params: params}
	body, _ := json.Marshal(req)

	var lastErr error
	for _, idx := range targets {
		entry := r.Plan.VMs[idx]
		url := entry.AgentURL() + "/fault/start"
		resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
		if err != nil {
			lastErr = fmt.Errorf("VM %s: %w", entry.Name, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("VM %s: agent returned %d", entry.Name, resp.StatusCode)
		}
	}
	return lastErr
}

func (r *Runner) stopFault(ctx context.Context, f Fault) {
	targets, err := ResolveTargets(f.Target, r.Plan)
	if err != nil {
		return
	}
	body, _ := json.Marshal(map[string]string{"id": f.ID})
	for _, idx := range targets {
		entry := r.Plan.VMs[idx]
		url := entry.AgentURL() + "/fault/stop"
		resp, err := http.Post(url, "application/json", bytes.NewReader(body)) //nolint:noctx
		if err == nil {
			resp.Body.Close()
		}
	}
}

// ── Objective checking ────────────────────────────────────────────────────────

func (r *Runner) checkAllObjectives(ctx context.Context) bool {
	for _, obj := range r.Scenario.Objectives {
		if !r.checkObjective(ctx, obj) {
			return false
		}
	}
	return len(r.Scenario.Objectives) > 0
}

func (r *Runner) checkObjective(ctx context.Context, obj Objective) bool {
	check := obj.Check
	targets, err := ResolveTargets(check.Target, r.Plan)
	if err != nil {
		return false
	}

	for _, idx := range targets {
		entry := r.Plan.VMs[idx]
		switch check.Type {
		case CheckHTTP:
			url := "http://" + fmt.Sprintf("172.16.%d.2", entry.Index) + check.Path
			resp, err := http.Get(url) //nolint:noctx
			if err != nil {
				return false
			}
			resp.Body.Close()
			want := check.ExpectedStatus
			if want == 0 {
				want = 200
			}
			if resp.StatusCode != want {
				return false
			}

		case CheckAgentMetric:
			metrics, err := r.fetchMetrics(entry)
			if err != nil {
				return false
			}
			if !evalCondition(metrics, check.Metric, check.Condition) {
				return false
			}

		case CheckProcessActive:
			services, err := r.fetchServices(entry)
			if err != nil {
				return false
			}
			found := false
			for _, svc := range services {
				if svc.Name == check.Service && svc.Active {
					found = true
					break
				}
			}
			if !found {
				return false
			}

		case CheckFileExists:
			// Best-effort: check via agent metrics (not directly supported; skip)
		}
	}
	return true
}

type agentMetrics struct {
	CPUPct      float64 `json:"cpu_pct"`
	MemUsedPct  float64 `json:"mem_used_pct"`
	DiskUsedPct float64 `json:"disk_used_pct"`
	DiskFreeMB  float64 `json:"disk_free_mb"`
	LoadAvg1    float64 `json:"load_avg_1"`
}

type agentService struct {
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

func (r *Runner) fetchMetrics(entry VMEntry) (*agentMetrics, error) {
	resp, err := http.Get(entry.AgentURL() + "/metrics") //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var m agentMetrics
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *Runner) fetchServices(entry VMEntry) ([]agentService, error) {
	resp, err := http.Get(entry.AgentURL() + "/services") //nolint:noctx
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var svcs []agentService
	if err := json.NewDecoder(resp.Body).Decode(&svcs); err != nil {
		return nil, err
	}
	return svcs, nil
}

// evalCondition evaluates simple expressions like "disk_used_pct < 80".
func evalCondition(m *agentMetrics, metric, condition string) bool {
	var val float64
	switch metric {
	case "cpu_pct":
		val = m.CPUPct
	case "mem_used_pct":
		val = m.MemUsedPct
	case "disk_used_pct":
		val = m.DiskUsedPct
	case "disk_free_mb":
		val = m.DiskFreeMB
	case "load_avg_1":
		val = m.LoadAvg1
	default:
		return false
	}

	// condition: "< 80%" or "> 50" or "< 80"
	cond := strings.TrimSpace(condition)
	cond = strings.TrimSuffix(cond, "%")
	cond = strings.TrimSpace(cond)

	if strings.HasPrefix(cond, "<") {
		thresh, err := strconv.ParseFloat(strings.TrimSpace(cond[1:]), 64)
		if err != nil {
			return false
		}
		return val < thresh
	}
	if strings.HasPrefix(cond, ">") {
		thresh, err := strconv.ParseFloat(strings.TrimSpace(cond[1:]), 64)
		if err != nil {
			return false
		}
		return val > thresh
	}
	return false
}

// ── SSH Provisioning ──────────────────────────────────────────────────────────

var sshConfig = &ssh.ClientConfig{
	User:            "root",
	Auth:            []ssh.AuthMethod{ssh.Password("root")},
	HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec
	Timeout:         5 * time.Second,
}

// provisionAll SSHes into every VM and installs its tier's services.
func (r *Runner) provisionAll(ctx context.Context) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(r.Plan.VMs))
	for _, entry := range r.Plan.VMs {
		var services, setup []string
		for _, tier := range r.Scenario.Environment.Tiers {
			if tier.Name == entry.Tier {
				services = tier.Services
				setup = tier.Setup
				break
			}
		}
		wg.Add(1)
		e := entry
		svcs := services
		go func() {
			defer wg.Done()
			if len(svcs) > 0 {
				fmt.Fprintf(os.Stderr, "  provisioning %s: installing %s...\n", e.Name, strings.Join(svcs, ", "))
			}
			if err := provisionVM(ctx, e, svcs, setup); err != nil {
				errs <- fmt.Errorf("%s: %w", e.Name, err)
			} else if len(svcs) > 0 {
				fmt.Fprintf(os.Stderr, "  ✓ %s provisioned (%s)\n", e.Name, strings.Join(svcs, ", "))
			}
		}()
	}
	wg.Wait()
	close(errs)
	var lastErr error
	for err := range errs {
		fmt.Fprintf(os.Stderr, "  provisioning warning: %v\n", err)
		lastErr = err
	}
	return lastErr
}

// provisionVM SSHes into a single VM, installs packages, and runs setup commands.
func provisionVM(ctx context.Context, entry VMEntry, services, setup []string) error {
	addr := fmt.Sprintf("172.16.%d.2:22", entry.Index)

	// Retry until SSH is up (VM may still be booting)
	var client *ssh.Client
	var err error
	for i := 0; i < 15; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		client, err = ssh.Dial("tcp", addr, sshConfig)
		if err == nil {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return fmt.Errorf("SSH connect: %w", err)
	}
	defer client.Close()

	// Install packages
	pkgs := servicePackages(services)
	if len(pkgs) > 0 {
		if err := runSSH(client, "apt-get update -qq"); err != nil {
			return fmt.Errorf("apt-get update: %w", err)
		}
		cmd := "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " + strings.Join(pkgs, " ")
		if err := runSSH(client, cmd); err != nil {
			return err
		}
	}
	for _, cmd := range setup {
		if err := runSSH(client, cmd); err != nil {
			return fmt.Errorf("setup command %q: %w", cmd, err)
		}
	}
	return nil
}

// servicePackages maps service names to apt package names.
func servicePackages(services []string) []string {
	mapping := map[string]string{
		"nginx":       "nginx",
		"apache2":     "apache2",
		"mysql":       "mysql-server",
		"postgresql":  "postgresql",
		"postgres":    "postgresql",
		"redis":       "redis-server",
		"redis-server": "redis-server",
		"app-server":  "python3 python3-flask",
		"celery":      "python3-celery",
		"stress-ng":   "stress-ng",
	}
	seen := map[string]bool{}
	var pkgs []string
	for _, svc := range services {
		if pkg, ok := mapping[svc]; ok {
			for _, p := range strings.Fields(pkg) {
				if !seen[p] {
					seen[p] = true
					pkgs = append(pkgs, p)
				}
			}
		}
	}
	return pkgs
}

// runSSH runs a single command on an established SSH client.
func runSSH(client *ssh.Client, cmd string) error {
	sess, err := client.NewSession()
	if err != nil {
		return err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	return nil
}

// buildMOTD formats the scenario context (title, description, alerts, symptoms,
// objectives) as a string suitable for /etc/motd on the VM.
func (r *Runner) buildMOTD() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== %s ===\n", r.Scenario.Meta.Title)
	fmt.Fprintf(&b, "Difficulty: %s | Architecture: %s\n\n", r.Scenario.Meta.Difficulty, r.Scenario.Meta.Architecture)
	fmt.Fprintf(&b, "%s\n", strings.TrimSpace(r.Scenario.Meta.Description))

	fmt.Fprintf(&b, "\n--- Active Alerts ---\n")
	for _, a := range r.Scenario.Signals.Alerts {
		fmt.Fprintf(&b, "[%s] %s — %s\n", strings.ToUpper(a.Severity), a.Name, a.Message)
	}

	if len(r.Scenario.Signals.Symptoms) > 0 {
		fmt.Fprintf(&b, "\n--- What you're seeing ---\n")
		for _, s := range r.Scenario.Signals.Symptoms {
			fmt.Fprintf(&b, "  • %s\n", s)
		}
	}

	fmt.Fprintf(&b, "\nObjectives:\n")
	for _, obj := range r.Scenario.Objectives {
		fmt.Fprintf(&b, "  [ ] %s\n", obj.Description)
	}

	fmt.Fprintf(&b, "\nType 'pudu hint' for a hint (-%d pts each).\n\n", r.Scenario.Scoring.HintPenalty)
	return b.String()
}

// ── Output helpers ────────────────────────────────────────────────────────────

func (r *Runner) printSignals() {
	fmt.Println("\n--- Active Alerts ---")
	for _, a := range r.Scenario.Signals.Alerts {
		fmt.Printf("[%s] %s — %s\n", strings.ToUpper(a.Severity), a.Name, a.Message)
	}
	if len(r.Scenario.Signals.Symptoms) > 0 {
		fmt.Println("\n--- What you're seeing ---")
		for _, s := range r.Scenario.Signals.Symptoms {
			fmt.Printf("  • %s\n", s)
		}
	}
	if r.Scenario.Signals.RunbookURL != "" {
		fmt.Printf("\nRunbook: %s\n", r.Scenario.Signals.RunbookURL)
	}
	fmt.Printf("\nObjectives:\n")
	for _, obj := range r.Scenario.Objectives {
		fmt.Printf("  [ ] %s\n", obj.Description)
	}
	fmt.Printf("\nType 'pudu hint' for a hint (-%d pts each).\n\n", r.Scenario.Scoring.HintPenalty)
}

func (r *Runner) printResult(solved bool) {
	elapsed := r.score.Elapsed.Round(time.Second)
	score := r.score.Current()
	fmt.Printf("\n=== %s ===\n", func() string {
		if solved {
			return "SCENARIO COMPLETE"
		}
		return "SCENARIO ABORTED"
	}())
	fmt.Printf("Time elapsed : %s\n", elapsed)
	fmt.Printf("Hints used   : %d\n", r.hintsUsed)
	fmt.Printf("Final score  : %d / %d\n", score, r.Scenario.Scoring.Base)
}
