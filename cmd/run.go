package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pathcl/pudu/vm"
)

// Execute parses args and runs the appropriate subcommand.
func Execute() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: pudu <command> [flags]\n\nCommands:\n  run       Launch a Firecracker microVM\n  serve     Launch VMs and start WebSSH server\n  scenario  Run an incident simulation scenario\n")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		runCmd(os.Args[2:])
	case "serve":
		serveCmd(os.Args[2:])
	case "scenario":
		scenarioCmd(os.Args[2:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runCmd(args []string) {
	cfg := vm.DefaultConfig()
	var count int
	var cloudInitConfig string

	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.StringVar(&cfg.FirecrackerBin, "firecracker-bin", "", "path to firecracker binary (overrides FIRECRACKER_BIN env and PATH)")
	fs.StringVar(&cfg.SocketPath, "socket", cfg.SocketPath, "Firecracker API socket path")
	fs.StringVar(&cfg.KernelImagePath, "kernel", "", "path to uncompressed vmlinux kernel image (required)")
	fs.StringVar(&cfg.RootFSPath, "rootfs", "", "path to ext4 root filesystem image (required)")
	fs.Int64Var(&cfg.VCPUs, "vcpus", cfg.VCPUs, "number of vCPUs")
	fs.Int64Var(&cfg.MemSizeMiB, "mem", cfg.MemSizeMiB, "memory in MiB")
	fs.Int64Var(&cfg.RootFSSizeMiB, "rootfs-size", cfg.RootFSSizeMiB, "rootfs size in MiB (0 = no resize)")
	fs.StringVar(&cfg.KernelArgs, "kernel-args", cfg.KernelArgs, "kernel boot arguments")
	fs.StringVar(&cfg.TapDeviceName, "tap", "", "TAP device name for networking (device must be pre-created)")
	fs.StringVar(&cfg.MacAddress, "mac", "", "MAC address for the VM network interface")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level (Trace, Debug, Info, Warn, Error)")
	fs.StringVar(&cfg.CloudInitISO, "cloud-init-iso", "", "path to cloud-init NoCloud ISO image (base path for multi-VM)")
	fs.StringVar(&cloudInitConfig, "cloud-init-config", "cloud-init-config.yaml", "path to cloud-init config template")
	fs.IntVar(&count, "count", 1, "number of VMs to launch")
	fs.Parse(args) //nolint:errcheck

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// Generate per-VM cloud-init ISOs if count > 1
	if count > 1 && cfg.CloudInitISO != "" {
		if err := generatePerVMCloudInitISOs(cfg.CloudInitISO, cloudInitConfig, count); err != nil {
			fmt.Fprintf(os.Stderr, "error generating cloud-init ISOs: %v\n", err)
			os.Exit(1)
		}
	}

	if count == 1 {
		runSingleVM(ctx, cfg)
	} else {
		if err := launchFleet(ctx, cfg, count); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}
}

func runSingleVM(ctx context.Context, cfg vm.Config) {
	m, err := vm.New(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer m.Stop()

	if err := m.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start VM: %v\n", err)
		os.Exit(1)
	}

	if err := m.Wait(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintf(os.Stderr, "VM exited with error: %v\n", err)
		os.Exit(1)
	}
}
