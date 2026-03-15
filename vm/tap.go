package vm

import (
	"fmt"
	"os/exec"
)

// EnsureTAP idempotently creates tap{id} with host IP 172.16.{id}.1/30,
// enables IP forwarding, and adds a MASQUERADE rule so the VM can reach
// the internet. Safe to call concurrently for different IDs.
func EnsureTAP(id int) error {
	tap := fmt.Sprintf("tap%d", id)
	hostIP := fmt.Sprintf("172.16.%d.1/30", id)

	cmds := [][]string{
		{"ip", "tuntap", "add", tap, "mode", "tap"},
		{"ip", "addr", "add", hostIP, "dev", tap},
		{"ip", "link", "set", tap, "up"},
		{"sh", "-c", "echo 1 > /proc/sys/net/ipv4/ip_forward"},
	}

	for _, args := range cmds {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			// "already exists" errors are fine — treat them as no-ops
			msg := string(out)
			if isAlreadyExists(msg) {
				continue
			}
			return fmt.Errorf("tap setup %v: %w\n%s", args, err, msg)
		}
	}

	// Idempotent MASQUERADE rule: check before adding.
	// -w makes iptables wait for the xtables lock instead of failing
	// with exit code 4 when multiple goroutines call this concurrently.
	check := exec.Command("iptables", "-w", "-t", "nat", "-C", "POSTROUTING", "-j", "MASQUERADE")
	if err := check.Run(); err != nil {
		add := exec.Command("iptables", "-w", "-t", "nat", "-A", "POSTROUTING", "-j", "MASQUERADE")
		if out, err := add.CombinedOutput(); err != nil {
			return fmt.Errorf("iptables MASQUERADE: %w\n%s", err, out)
		}
	}

	return nil
}

// RemoveTAP tears down tap{id}.
func RemoveTAP(id int) {
	exec.Command("ip", "link", "del", fmt.Sprintf("tap%d", id)).Run() //nolint:errcheck
}

func isAlreadyExists(msg string) bool {
	for _, s := range []string{
		"already exists",
		"RTNETLINK answers: File exists",
		"File exists",
		"Device or resource busy",
		"TUNSETIFF",
	} {
		if contains(msg, s) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
