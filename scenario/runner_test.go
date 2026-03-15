// White-box tests for unexported helpers in the scenario package.
package scenario

import (
	"testing"
)

func TestEvalCondition(t *testing.T) {
	m := &agentMetrics{
		CPUPct:      75.0,
		MemUsedPct:  60.0,
		DiskUsedPct: 50.0,
		DiskFreeMB:  200.0,
		LoadAvg1:    1.5,
	}

	tests := []struct {
		metric    string
		condition string
		want      bool
	}{
		{"cpu_pct", "< 80", true},
		{"cpu_pct", "< 70", false},
		{"cpu_pct", "> 70", true},
		{"cpu_pct", "> 80", false},
		{"mem_used_pct", "< 80", true},
		{"disk_used_pct", "< 80%", true},  // percent sign stripped
		{"disk_free_mb", "> 100", true},
		{"load_avg_1", "< 2.0", true},
		{"unknown_metric", "< 50", false}, // unknown metric → false
	}

	for _, tc := range tests {
		t.Run(tc.metric+tc.condition, func(t *testing.T) {
			if got := evalCondition(m, tc.metric, tc.condition); got != tc.want {
				t.Errorf("evalCondition(%q, %q) = %v, want %v", tc.metric, tc.condition, got, tc.want)
			}
		})
	}
}

func TestServicePackages(t *testing.T) {
	tests := []struct {
		services []string
		want     []string
	}{
		{[]string{"nginx"}, []string{"nginx"}},
		{[]string{"postgres"}, []string{"postgresql"}},
		{[]string{"redis"}, []string{"redis-server"}},
		{[]string{"app-server"}, []string{"python3", "python3-flask"}},
		{[]string{"unknown-svc"}, nil}, // not in mapping → empty
		{[]string{"nginx", "nginx"}, []string{"nginx"}}, // deduplication
	}

	for _, tc := range tests {
		t.Run(tc.services[0], func(t *testing.T) {
			got := servicePackages(tc.services)
			if len(got) != len(tc.want) {
				t.Errorf("servicePackages(%v) = %v, want %v", tc.services, got, tc.want)
				return
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("servicePackages[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
