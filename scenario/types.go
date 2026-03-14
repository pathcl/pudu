package scenario

import (
	"fmt"
	"time"
)

// Architecture of the simulated system.
type Architecture string

const (
	ArchMonolith      Architecture = "monolith"
	ArchMicroservices Architecture = "microservices"
	ArchBoth          Architecture = "both"
)

// Difficulty of the scenario.
type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
	DifficultyExpert Difficulty = "expert"
)

// Scenario is the top-level parsed scenario document.
type Scenario struct {
	Meta        ScenarioMeta `yaml:"scenario"`
	Environment Environment  `yaml:"environment"`
	Faults      []Fault      `yaml:"faults"`
	Signals     Signals      `yaml:"signals"`
	Objectives  []Objective  `yaml:"objectives"`
	Hints       []Hint       `yaml:"hints"`
	Scoring     Scoring      `yaml:"scoring"`
}

// ScenarioMeta holds identifying information.
type ScenarioMeta struct {
	ID           string       `yaml:"id"`
	Title        string       `yaml:"title"`
	Difficulty   Difficulty   `yaml:"difficulty"`
	Architecture Architecture `yaml:"architecture"`
	Tags         []string     `yaml:"tags"`
	Description  string       `yaml:"description"`
}

// Environment describes the VM topology for the scenario.
type Environment struct {
	Extends string `yaml:"extends"` // path to architecture template YAML
	Tiers   []Tier `yaml:"tiers"`
}

// Tier is a named role class of VMs (e.g., "web", "database").
type Tier struct {
	Name     string   `yaml:"name"`
	Count    int      `yaml:"count"`
	CountMin int      `yaml:"count_min"`
	CountMax int      `yaml:"count_max"`
	VCPUs    int64    `yaml:"vcpus"`
	MemMB    int64    `yaml:"mem_mb"`
	Services []string `yaml:"services"` // apt packages to install
	Setup    []string `yaml:"setup"`    // shell commands to run after install
}

// FaultType identifies the category of fault to inject.
type FaultType string

const (
	FaultCPU        FaultType = "cpu"
	FaultMemory     FaultType = "memory"
	FaultDisk       FaultType = "disk"
	FaultNetwork    FaultType = "network"
	FaultProcess    FaultType = "process"
	FaultDNS        FaultType = "dns"
	FaultFilesystem FaultType = "filesystem"
	FaultResource   FaultType = "resource"
)

// FaultTarget identifies which VM(s) receive a fault.
type FaultTarget struct {
	VM     string `yaml:"vm"`     // specific VM name, e.g., "web-0"
	Tier   string `yaml:"tier"`   // all VMs in tier, e.g., "web"
	Select string `yaml:"select"` // "all" (default), "random(N)", "primary"
}

// FaultTrigger makes a fault conditional on another fault or metric.
type FaultTrigger struct {
	Fault string `yaml:"fault"` // ID of the fault that causes this one
	When  string `yaml:"when"`  // "immediately" | "disk_usage > 90%" | etc.
}

// Fault describes a single failure to inject.
type Fault struct {
	ID       string            `yaml:"id"`
	Type     FaultType         `yaml:"type"`
	Target   FaultTarget       `yaml:"target"`
	Params   map[string]string `yaml:"params"`
	At       string            `yaml:"at"`       // duration from start, e.g., "0s", "5m"
	Duration string            `yaml:"duration"` // auto-recover after this long
	Triggers []FaultTrigger    `yaml:"triggers"` // if set, fault fires on trigger not At
}

// AtDuration parses the At field.
func (f *Fault) AtDuration() (time.Duration, error) {
	if f.At == "" {
		return 0, nil
	}
	return time.ParseDuration(f.At)
}

// DurationDuration parses the Duration field.
func (f *Fault) DurationDuration() (time.Duration, error) {
	if f.Duration == "" {
		return 0, nil
	}
	return time.ParseDuration(f.Duration)
}

// Alert is a simulated monitoring alert shown to the trainee.
type Alert struct {
	Name     string `yaml:"name"`
	Severity string `yaml:"severity"` // critical, warning, info
	FiredAt  string `yaml:"fired_at"` // duration from start
	Message  string `yaml:"message"`
}

// Signals is what the on-call trainee sees at scenario start.
type Signals struct {
	Alerts     []Alert  `yaml:"alerts"`
	Symptoms   []string `yaml:"symptoms"`
	RunbookURL string   `yaml:"runbook_url"`
}

// CheckType identifies the kind of objective verification.
type CheckType string

const (
	CheckHTTP          CheckType = "http"
	CheckFileExists    CheckType = "file-exists"
	CheckAgentMetric   CheckType = "agent-metric"
	CheckProcessActive CheckType = "process-running"
)

// ObjectiveCheck describes how to verify an objective.
type ObjectiveCheck struct {
	Type           CheckType   `yaml:"type"`
	Target         FaultTarget `yaml:"target"`
	Path           string      `yaml:"path"`
	ExpectedStatus int         `yaml:"expected_status"`
	Window         string      `yaml:"window"`   // must pass for this long
	Metric         string      `yaml:"metric"`   // e.g., "disk_usage"
	Condition      string      `yaml:"condition"` // e.g., "< 80%"
	Service        string      `yaml:"service"`  // for process-running check
}

// Objective is a condition that must be met to complete the scenario.
type Objective struct {
	ID          string         `yaml:"id"`
	Description string         `yaml:"description"`
	Check       ObjectiveCheck `yaml:"check"`
}

// Hint is shown to the trainee on demand (costs score points).
type Hint struct {
	Text string `yaml:"text"`
}

// Scoring defines how points are calculated.
type Scoring struct {
	Base                 int     `yaml:"base"`
	TimePenaltyPerSecond float64 `yaml:"time_penalty_per_second"`
	HintPenalty          int     `yaml:"hint_penalty"`
	PerfectWindow        string  `yaml:"perfect_window"` // e.g., "5m"
}

func DefaultScoring() Scoring {
	return Scoring{
		Base:                 100,
		TimePenaltyPerSecond: 0.05,
		HintPenalty:          10,
		PerfectWindow:        "5m",
	}
}

// Score tracks the trainee's current score.
type Score struct {
	Base       int
	Elapsed    time.Duration
	HintsUsed  int
	HintPenalty int
	TimePenaltyPerSecond float64
}

func (s *Score) Current() int {
	timePenalty := int(s.Elapsed.Seconds() * s.TimePenaltyPerSecond)
	hintPenalty := s.HintsUsed * s.HintPenalty
	result := s.Base - timePenalty - hintPenalty
	if result < 0 {
		return 0
	}
	return result
}

// VMPlan is the resolved mapping of tiers to VM indices.
type VMPlan struct {
	TotalVMs int
	VMs      []VMEntry
	ByName   map[string]int    // "web-0" → vmIndex
	ByTier   map[string][]int  // "web" → [0, 1]
}

// VMEntry is a single resolved VM in the plan.
type VMEntry struct {
	Index     int
	Name      string // "web-0"
	Tier      string // "web"
	TierIndex int    // 0 (first in tier)
	VCPUs     int64
	MemMB     int64
	Services  []string
}

// AgentURL returns the HTTP base URL for the agent on this VM.
func (e *VMEntry) AgentURL() string {
	return fmt.Sprintf("http://172.16.%d.2:7777", e.Index)
}

// RunOptions are passed to the Runner at creation time.
type RunOptions struct {
	KernelPath     string
	RootFSPath     string
	CloudInitISO   string
	FirecrackerBin string
	ScaleOverrides map[string]int // tier name → count override
	DryRun         bool
	WebPort        int // port for WebSSH terminal (0 = disabled)
}
