package vm

import (
	"fmt"
	"os"
	"os/exec"
)

// EnsureCloudInitISO generates a per-VM cloud-init ISO at dstPath if it does
// not already exist, using the make-cloud-init-iso.sh script with the given
// config template and hostname.
//
// dstPath  — target ISO path, e.g. "cloud-init-3.iso"
// srcConfig — cloud-init config template, e.g. "cloud-init-config.yaml"
// hostname  — VM hostname to inject, e.g. "vm-3"
func EnsureCloudInitISO(dstPath, srcConfig, hostname string) error {
	if _, err := os.Stat(dstPath); err == nil {
		return nil // already exists
	}
	out, err := exec.Command("bash", "make-cloud-init-iso.sh", dstPath, srcConfig, hostname).CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate cloud-init ISO %s: %w\n%s", dstPath, err, out)
	}
	return nil
}
