package vm

import (
	"testing"
)

func TestIsAlreadyExists(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		// These mean the device exists cleanly — safe to continue
		{"RTNETLINK answers: File exists\n", true},
		{"File exists", true},
		{"already exists", true},

		// These indicate the device is busy/broken — NOT safe to continue
		{"Device or resource busy", false},
		{"TUNSETIFF: Device or resource busy", false},
		{"TUNSETIFF", false},

		// Unrelated errors
		{"permission denied", false},
		{"no such file or directory", false},
		{"", false},
	}

	for _, tc := range tests {
		got := isAlreadyExists(tc.msg)
		if got != tc.want {
			t.Errorf("isAlreadyExists(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}
