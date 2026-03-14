package vm

import (
	"fmt"
	"os"
	"os/exec"
)

// PrepareRootFS creates a per-VM copy of the base rootfs with /etc/hostname set
// to "vm-N" and resized to sizeMiB. Uses cp --reflink=auto for COW efficiency.
// The caller is responsible for removing the returned path when the VM stops.
func PrepareRootFS(baseRootFS string, id int, sizeMiB int64) (string, error) {
	vmRootFS := fmt.Sprintf("vm-%d-rootfs.ext4", id)

	// Create a copy (COW if filesystem supports it, otherwise full copy)
	if out, err := exec.Command("cp", "--reflink=auto", baseRootFS, vmRootFS).CombinedOutput(); err != nil {
		// reflink not supported, fall back to regular cp
		if out2, err2 := exec.Command("cp", baseRootFS, vmRootFS).CombinedOutput(); err2 != nil {
			return "", fmt.Errorf("failed to copy rootfs: %v\n%s", err2, out2)
		}
		_ = out
	}

	// Write /etc/hostname into the copy via debugfs
	hostname := fmt.Sprintf("vm-%d\n", id)
	tmp, err := os.CreateTemp("", "hostname-*")
	if err != nil {
		os.Remove(vmRootFS)
		return "", fmt.Errorf("failed to create temp hostname file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(hostname); err != nil {
		tmp.Close()
		os.Remove(vmRootFS)
		return "", err
	}
	tmp.Close()

	// Use a script file so we can run multiple debugfs commands:
	// 1. rm (so write doesn't fail on existing file)
	// 2. write the new content
	script, err := os.CreateTemp("", "debugfs-script-*")
	if err != nil {
		os.Remove(vmRootFS)
		return "", fmt.Errorf("failed to create debugfs script: %w", err)
	}
	defer os.Remove(script.Name())
	fmt.Fprintf(script, "cd /etc\nrm hostname\nwrite %s hostname\n", tmp.Name())
	script.Close()

	cmd := exec.Command("debugfs", "-w", "-f", script.Name(), vmRootFS)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.Remove(vmRootFS)
		return "", fmt.Errorf("debugfs write failed: %v\n%s", err, out)
	}

	// Expand the image if a larger size is requested
	if sizeMiB > 0 {
		sizeArg := fmt.Sprintf("%dM", sizeMiB)
		if out, err := exec.Command("truncate", "-s", sizeArg, vmRootFS).CombinedOutput(); err != nil {
			os.Remove(vmRootFS)
			return "", fmt.Errorf("truncate failed: %v\n%s", err, out)
		}
		// e2fsck is required before resize2fs; -y auto-answers yes to all prompts
		exec.Command("e2fsck", "-f", "-y", vmRootFS).Run() //nolint:errcheck
		if out, err := exec.Command("resize2fs", vmRootFS).CombinedOutput(); err != nil {
			os.Remove(vmRootFS)
			return "", fmt.Errorf("resize2fs failed: %v\n%s", err, out)
		}
	}

	return vmRootFS, nil
}
