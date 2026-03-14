package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/pathcl/pudu/vm"
)

// generatePerVMCloudInitISOs creates per-VM cloud-init ISOs with unique hostnames
func generatePerVMCloudInitISOs(baseISO, configPath string, count int) error {
	for i := 0; i < count; i++ {
		// Get the output ISO path for this VM
		isoPath := fmt.Sprintf("%s-%d.iso", baseISO[:len(baseISO)-4], i)
		hostname := fmt.Sprintf("vm-%d", i)

		fmt.Fprintf(os.Stderr, "Generating cloud-init ISO for %s (hostname: %s)\n", isoPath, hostname)

		// Run make-cloud-init-iso.sh with hostname parameter
		cmd := exec.Command("bash", "make-cloud-init-iso.sh", isoPath, configPath, hostname)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to generate cloud-init ISO for VM %d: %v\n%s", i, err, output)
		}
		fmt.Fprintf(os.Stderr, "✓ Generated %s\n", isoPath)
	}
	return nil
}

// launchFleet launches N VMs in parallel using goroutines.
func launchFleet(ctx context.Context, baseCfg vm.Config, count int) error {
	// Print VM information upfront
	fmt.Fprintf(os.Stderr, "\nLaunching %d VM(s):\n", count)
	for i := 0; i < count; i++ {
		ip := fmt.Sprintf("172.16.%d.2", i)
		logFile := fmt.Sprintf("vm-%d.log", i)
		fmt.Fprintf(os.Stderr, "  VM %d: ssh root@%s (logs: %s)\n", i, ip, logFile)
	}
	fmt.Fprintf(os.Stderr, "\n")

	var wg sync.WaitGroup
	errChan := make(chan error, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			cfg := baseCfg.ForVM(id)

			// Prepare a per-VM rootfs with /etc/hostname patched to "vm-N"
			vmRootFS, err := vm.PrepareRootFS(baseCfg.RootFSPath, id, baseCfg.RootFSSizeMiB)
			if err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: failed to prepare rootfs: %w", id, err)
				}
				return
			}
			defer os.Remove(vmRootFS)
			cfg.RootFSPath = vmRootFS

			m, err := vm.New(ctx, cfg)
			if err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: failed to create: %w", id, err)
				}
				return
			}
			defer m.Stop()

			if err := m.Start(ctx); err != nil {
				if ctx.Err() == nil {
					errChan <- fmt.Errorf("VM %d: failed to start: %w", id, err)
				}
				return
			}

			if err := m.Wait(ctx); err != nil && ctx.Err() == nil {
				errChan <- fmt.Errorf("VM %d: exited with error: %w", id, err)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Collect errors
	var lastErr error
	for err := range errChan {
		lastErr = err
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}

	return lastErr
}
