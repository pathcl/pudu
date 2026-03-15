package scenario_test

import (
	"testing"
	"time"

	"github.com/pathcl/pudu/scenario"
)

func TestScore_Current(t *testing.T) {
	tests := []struct {
		name  string
		score scenario.Score
		want  int
	}{
		{
			name:  "no penalty",
			score: scenario.Score{Base: 100},
			want:  100,
		},
		{
			name:  "hint penalty",
			score: scenario.Score{Base: 100, HintsUsed: 2, HintPenalty: 10},
			want:  80,
		},
		{
			name: "time penalty",
			score: scenario.Score{
				Base:                 100,
				Elapsed:              60 * time.Second,
				TimePenaltyPerSecond: 0.5,
			},
			want: 70,
		},
		{
			name: "combined penalty",
			score: scenario.Score{
				Base:                 100,
				HintsUsed:            1,
				HintPenalty:          10,
				Elapsed:              20 * time.Second,
				TimePenaltyPerSecond: 0.5,
			},
			want: 80,
		},
		{
			name:  "never below zero",
			score: scenario.Score{Base: 10, HintsUsed: 5, HintPenalty: 100},
			want:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.score.Current(); got != tc.want {
				t.Errorf("Current() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFault_AtDuration(t *testing.T) {
	tests := []struct {
		at      string
		wantErr bool
		want    time.Duration
	}{
		{"0s", false, 0},
		{"5m", false, 5 * time.Minute},
		{"1h30m", false, 90 * time.Minute},
		{"", false, 0},
		{"invalid", true, 0},
	}

	for _, tc := range tests {
		t.Run(tc.at, func(t *testing.T) {
			f := scenario.Fault{At: tc.at}
			got, err := f.AtDuration()
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("AtDuration() = %v, want %v", got, tc.want)
			}
		})
	}
}
