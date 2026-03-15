package vm

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

func TestForVM_PerVMFields(t *testing.T) {
	tests := []struct {
		name  string
		vmID  int
		want  Config
	}{
		{
			name: "vm 0",
			vmID: 0,
			want: Config{
				SocketPath:    "/tmp/firecracker-0.sock",
				TapDeviceName: "tap0",
				MacAddress:    "AA:FC:00:00:00:01",
				LogPath:       "vm-0.log",
			},
		},
		{
			name: "vm 3",
			vmID: 3,
			want: Config{
				SocketPath:    "/tmp/firecracker-3.sock",
				TapDeviceName: "tap3",
				MacAddress:    "AA:FC:00:00:00:04",
				LogPath:       "vm-3.log",
			},
		},
	}

	base := Config{KernelImagePath: "k", RootFSPath: "r", VCPUs: 1, MemSizeMiB: 128}
	ignore := cmpopts.IgnoreFields(Config{},
		"KernelImagePath", "RootFSPath", "VCPUs", "MemSizeMiB",
		"KernelArgs", "FirecrackerBin", "CloudInitISO",
	)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := base.ForVM(tc.vmID)
			if diff := cmp.Diff(tc.want, got, ignore); diff != "" {
				t.Errorf("ForVM(%d) mismatch (-want +got):\n%s", tc.vmID, diff)
			}
		})
	}
}

func TestForVM_KernelArgsIP(t *testing.T) {
	base := Config{
		KernelImagePath: "k",
		RootFSPath:      "r",
		VCPUs:           1,
		MemSizeMiB:      128,
		KernelArgs:      "console=ttyS0 reboot=k panic=1 pci=off",
	}

	tests := []struct {
		vmID    int
		wantIP  string
		wantGW  string
	}{
		{0, "172.16.0.2", "172.16.0.1"},
		{5, "172.16.5.2", "172.16.5.1"},
	}

	for _, tc := range tests {
		t.Run("", func(t *testing.T) {
			got := base.ForVM(tc.vmID)
			if !strings.Contains(got.KernelArgs, tc.wantIP) {
				t.Errorf("KernelArgs %q missing VM IP %s", got.KernelArgs, tc.wantIP)
			}
			if !strings.Contains(got.KernelArgs, tc.wantGW) {
				t.Errorf("KernelArgs %q missing gateway IP %s", got.KernelArgs, tc.wantGW)
			}
		})
	}
}

func TestForVM_UniqueIPsAcrossVMs(t *testing.T) {
	base := Config{KernelImagePath: "k", RootFSPath: "r", VCPUs: 1, MemSizeMiB: 128}
	seen := make(map[string]int)
	for id := 0; id < 10; id++ {
		cfg := base.ForVM(id)
		ip := fmt.Sprintf("172.16.%d.2", id)
		if prev, ok := seen[ip]; ok {
			t.Errorf("IP %s assigned to both vm %d and vm %d", ip, prev, id)
		}
		seen[ip] = id
		if !strings.Contains(cfg.KernelArgs, ip) {
			t.Errorf("vm %d: KernelArgs missing expected IP %s", id, ip)
		}
	}
}

func TestForVM_CloudInitISOPerVM(t *testing.T) {
	base := Config{
		KernelImagePath: "k",
		RootFSPath:      "r",
		VCPUs:           1,
		MemSizeMiB:      128,
		CloudInitISO:    "cloud-init.iso",
	}
	got := base.ForVM(2)
	want := "cloud-init-2.iso"
	if got.CloudInitISO != want {
		t.Errorf("CloudInitISO = %q, want %q", got.CloudInitISO, want)
	}
}

func TestForVM_NoCloudInitISO(t *testing.T) {
	base := Config{KernelImagePath: "k", RootFSPath: "r", VCPUs: 1, MemSizeMiB: 128}
	got := base.ForVM(0)
	if got.CloudInitISO != "" {
		t.Errorf("CloudInitISO = %q, want empty", got.CloudInitISO)
	}
}

func TestValidate_MissingKernel(t *testing.T) {
	cfg := Config{
		FirecrackerBin: "/usr/bin/true",
		RootFSPath:     "/dev/null",
		VCPUs:          1,
		MemSizeMiB:     128,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing kernel, got nil")
	}
}

func TestValidate_MissingRootFS(t *testing.T) {
	cfg := Config{
		FirecrackerBin:  "/usr/bin/true",
		KernelImagePath: "/dev/null",
		VCPUs:           1,
		MemSizeMiB:      128,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing rootfs, got nil")
	}
}

func TestValidate_InvalidVCPUs(t *testing.T) {
	cfg := Config{
		FirecrackerBin:  "/usr/bin/true",
		KernelImagePath: "/dev/null",
		RootFSPath:      "/dev/null",
		VCPUs:           0,
		MemSizeMiB:      128,
	}
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for vcpus=0, got nil")
	}
}

func TestDefaultConfig_Sensible(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.VCPUs < 1 {
		t.Errorf("DefaultConfig VCPUs = %d, want >= 1", cfg.VCPUs)
	}
	if cfg.MemSizeMiB < 128 {
		t.Errorf("DefaultConfig MemSizeMiB = %d, want >= 128", cfg.MemSizeMiB)
	}
	if cfg.SocketPath == "" {
		t.Error("DefaultConfig SocketPath is empty")
	}
}
