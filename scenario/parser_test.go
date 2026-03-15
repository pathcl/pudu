package scenario_test

import (
	"testing"

	"github.com/pathcl/pudu/scenario"
)

func TestLoad_ValidScenario(t *testing.T) {
	s, err := scenario.Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if s.Meta.ID != "test-001" {
		t.Errorf("Meta.ID = %q, want %q", s.Meta.ID, "test-001")
	}
	if len(s.Environment.Tiers) != 1 {
		t.Errorf("len(Tiers) = %d, want 1", len(s.Environment.Tiers))
	}
	if len(s.Faults) != 1 {
		t.Errorf("len(Faults) = %d, want 1", len(s.Faults))
	}
	if len(s.Hints) != 2 {
		t.Errorf("len(Hints) = %d, want 2", len(s.Hints))
	}
	if s.Scoring.Base != 100 {
		t.Errorf("Scoring.Base = %d, want 100", s.Scoring.Base)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	_, err := scenario.Load("testdata/does-not-exist.yaml")
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoad_DefaultScoring(t *testing.T) {
	// A scenario with no scoring block should get default scoring applied.
	s, err := scenario.Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if s.Scoring.TimePenaltyPerSecond == 0 {
		t.Error("expected non-zero TimePenaltyPerSecond from defaults")
	}
}

func TestBuildVMPlan_Basic(t *testing.T) {
	s, err := scenario.Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := scenario.BuildVMPlan(s, nil)
	if err != nil {
		t.Fatalf("BuildVMPlan() error: %v", err)
	}
	if plan.TotalVMs != 1 {
		t.Errorf("TotalVMs = %d, want 1", plan.TotalVMs)
	}
	if _, ok := plan.ByName["app-0"]; !ok {
		t.Error("expected VM named app-0")
	}
}

func TestBuildVMPlan_ScaleOverride(t *testing.T) {
	s, err := scenario.Load("testdata/minimal.yaml")
	if err != nil {
		t.Fatal(err)
	}

	plan, err := scenario.BuildVMPlan(s, map[string]int{"app": 3})
	if err != nil {
		t.Fatalf("BuildVMPlan() error: %v", err)
	}
	if plan.TotalVMs != 3 {
		t.Errorf("TotalVMs = %d, want 3", plan.TotalVMs)
	}
	if len(plan.ByTier["app"]) != 3 {
		t.Errorf("ByTier[app] = %v, want 3 entries", plan.ByTier["app"])
	}
}

func TestResolveTargets_ByVM(t *testing.T) {
	s, _ := scenario.Load("testdata/minimal.yaml")
	plan, _ := scenario.BuildVMPlan(s, nil)

	target := scenario.FaultTarget{VM: "app-0"}
	indices, err := scenario.ResolveTargets(target, plan)
	if err != nil {
		t.Fatalf("ResolveTargets() error: %v", err)
	}
	if len(indices) != 1 || indices[0] != 0 {
		t.Errorf("ResolveTargets() = %v, want [0]", indices)
	}
}

func TestResolveTargets_UnknownVM(t *testing.T) {
	s, _ := scenario.Load("testdata/minimal.yaml")
	plan, _ := scenario.BuildVMPlan(s, nil)

	_, err := scenario.ResolveTargets(scenario.FaultTarget{VM: "web-99"}, plan)
	if err == nil {
		t.Error("expected error for unknown VM, got nil")
	}
}
