package vm

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
)

// Config holds all parameters needed to launch a Firecracker microVM.
type Config struct {
	FirecrackerBin  string
	SocketPath      string
	KernelImagePath string
	RootFSPath      string
	VCPUs           int64
	MemSizeMiB      int64
	RootFSSizeMiB   int64
	KernelArgs      string
	TapDeviceName   string
	MacAddress      string
	LogPath         string
	LogLevel        string
	CloudInitISO    string
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		VCPUs:         1,
		MemSizeMiB:    512,
		RootFSSizeMiB: 1024,
		KernelArgs:    "console=ttyS0 reboot=k panic=1 pci=off",
		SocketPath:    "/tmp/firecracker.sock",
		LogLevel:      "Info",
	}
}

// resolveBinary resolves the Firecracker binary path using three-tier priority:
// 1. Explicitly set value (from flag)
// 2. FIRECRACKER_BIN environment variable
// 3. "firecracker" in $PATH
func resolveBinary(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if env := os.Getenv("FIRECRACKER_BIN"); env != "" {
		return env, nil
	}
	path, err := exec.LookPath("firecracker")
	if err != nil {
		return "", fmt.Errorf("firecracker binary not found in PATH; set --firecracker-bin or FIRECRACKER_BIN: %w", err)
	}
	return path, nil
}

// Validate checks that required fields are set and resolves the binary path.
func (c *Config) Validate() error {
	bin, err := resolveBinary(c.FirecrackerBin)
	if err != nil {
		return err
	}
	c.FirecrackerBin = bin

	if c.KernelImagePath == "" {
		return fmt.Errorf("--kernel is required")
	}
	if c.RootFSPath == "" {
		return fmt.Errorf("--rootfs is required")
	}
	if c.VCPUs < 1 {
		return fmt.Errorf("--vcpus must be >= 1")
	}
	if c.MemSizeMiB < 1 {
		return fmt.Errorf("--mem must be >= 1")
	}
	return nil
}

// ForVM returns a copy of the Config with per-VM resource overrides for the given VM index.
func (c Config) ForVM(id int) Config {
	copy := c
	copy.SocketPath = fmt.Sprintf("/tmp/firecracker-%d.sock", id)
	copy.TapDeviceName = fmt.Sprintf("tap%d", id)
	copy.MacAddress = fmt.Sprintf("AA:FC:00:00:00:%02X", id+1)
	copy.LogPath = fmt.Sprintf("vm-%d.log", id)

	// Update IP configuration in kernel args
	vmIP := fmt.Sprintf("172.16.%d.2", id)
	gwIP := fmt.Sprintf("172.16.%d.1", id)
	mask := "255.255.255.252" // /30 subnet
	newIPArg := fmt.Sprintf("ip=%s::%s:%s::eth0:off:8.8.8.8", vmIP, gwIP, mask)

	// Replace existing ip= arg or append new one
	re := regexp.MustCompile(`\bip=[^\s]*`)
	if re.MatchString(copy.KernelArgs) {
		copy.KernelArgs = re.ReplaceAllString(copy.KernelArgs, newIPArg)
	} else {
		copy.KernelArgs = copy.KernelArgs + " " + newIPArg
	}

	// Use per-VM cloud-init ISO if base path is provided
	if copy.CloudInitISO != "" {
		// Replace .iso extension with -N.iso to get per-VM ISO path
		if len(copy.CloudInitISO) > 4 && copy.CloudInitISO[len(copy.CloudInitISO)-4:] == ".iso" {
			copy.CloudInitISO = fmt.Sprintf("%s-%d.iso", copy.CloudInitISO[:len(copy.CloudInitISO)-4], id)
		}
	}

	return copy
}
