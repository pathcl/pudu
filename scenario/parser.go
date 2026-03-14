package scenario

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v2"
)

// Load reads and validates a scenario YAML file.
func Load(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading scenario file: %w", err)
	}
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parsing scenario YAML: %w", err)
	}
	if err := validate(&s); err != nil {
		return nil, fmt.Errorf("invalid scenario: %w", err)
	}
	return &s, nil
}

func validate(s *Scenario) error {
	if s.Meta.ID == "" {
		return fmt.Errorf("scenario.id is required")
	}
	if s.Meta.Title == "" {
		return fmt.Errorf("scenario.title is required")
	}
	if len(s.Environment.Tiers) == 0 {
		return fmt.Errorf("environment.tiers must not be empty")
	}
	for i := range s.Environment.Tiers {
		t := &s.Environment.Tiers[i]
		if t.Name == "" {
			return fmt.Errorf("tiers[%d].name is required", i)
		}
		if t.Count <= 0 {
			t.Count = 1
		}
		if t.VCPUs <= 0 {
			t.VCPUs = 1
		}
		if t.MemMB <= 0 {
			t.MemMB = 512
		}
	}
	for _, f := range s.Faults {
		if f.ID == "" {
			return fmt.Errorf("all faults must have an id")
		}
		if f.Target.VM == "" && f.Target.Tier == "" {
			return fmt.Errorf("fault %q: target.vm or target.tier is required", f.ID)
		}
	}
	// Apply default scoring
	if s.Scoring.Base == 0 {
		s.Scoring = DefaultScoring()
	}
	return nil
}

// BuildVMPlan resolves tiers into a concrete VM assignment, applying any
// scale overrides provided at runtime.
func BuildVMPlan(s *Scenario, overrides map[string]int) (*VMPlan, error) {
	plan := &VMPlan{
		ByName: make(map[string]int),
		ByTier: make(map[string][]int),
	}

	vmIndex := 0
	for _, tier := range s.Environment.Tiers {
		count := tier.Count
		if n, ok := overrides[tier.Name]; ok {
			if tier.CountMin > 0 && n < tier.CountMin {
				return nil, fmt.Errorf("tier %q: scale %d below count_min %d", tier.Name, n, tier.CountMin)
			}
			if tier.CountMax > 0 && n > tier.CountMax {
				return nil, fmt.Errorf("tier %q: scale %d above count_max %d", tier.Name, n, tier.CountMax)
			}
			count = n
		}
		for j := 0; j < count; j++ {
			name := fmt.Sprintf("%s-%d", tier.Name, j)
			entry := VMEntry{
				Index:     vmIndex,
				Name:      name,
				Tier:      tier.Name,
				TierIndex: j,
				VCPUs:     tier.VCPUs,
				MemMB:     tier.MemMB,
				Services:  tier.Services,
			}
			plan.VMs = append(plan.VMs, entry)
			plan.ByName[name] = vmIndex
			plan.ByTier[tier.Name] = append(plan.ByTier[tier.Name], vmIndex)
			vmIndex++
		}
	}
	plan.TotalVMs = vmIndex
	return plan, nil
}

// ResolveTargets returns VM indices for a given FaultTarget.
func ResolveTargets(target FaultTarget, plan *VMPlan) ([]int, error) {
	if target.VM != "" {
		idx, ok := plan.ByName[target.VM]
		if !ok {
			return nil, fmt.Errorf("unknown VM %q", target.VM)
		}
		return []int{idx}, nil
	}
	if target.Tier != "" {
		indices, ok := plan.ByTier[target.Tier]
		if !ok {
			return nil, fmt.Errorf("unknown tier %q", target.Tier)
		}
		if target.Select == "" || target.Select == "all" {
			return indices, nil
		}
		if target.Select == "random(1)" || target.Select == "random" {
			// Pick first for determinism in tests; real impl could randomise
			return []int{indices[0]}, nil
		}
		if target.Select == "primary" {
			return []int{indices[0]}, nil
		}
		return indices, nil
	}
	return nil, fmt.Errorf("target must specify vm or tier")
}
