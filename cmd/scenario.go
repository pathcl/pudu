package cmd

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/pathcl/pudu/scenario"
	"github.com/pathcl/pudu/vm"
)

func scenarioCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: pudu scenario <subcommand>\n\nSubcommands:\n  run    Run a scenario YAML file\n  hint   Request a hint for the running scenario\n")
		os.Exit(1)
	}
	switch args[0] {
	case "run":
		scenarioRunCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func scenarioRunCmd(args []string) {
	cfg := vm.DefaultConfig()
	var scaleStr string
	var dryRun bool
	var webPort int

	fs := flag.NewFlagSet("scenario run", flag.ExitOnError)
	fs.StringVar(&cfg.KernelImagePath, "kernel", "", "path to vmlinux kernel (required)")
	fs.StringVar(&cfg.RootFSPath, "rootfs", "", "path to ext4 rootfs (required)")
	fs.StringVar(&cfg.FirecrackerBin, "firecracker-bin", "", "path to firecracker binary")
	fs.StringVar(&cfg.CloudInitISO, "cloud-init-iso", "", "path to cloud-init ISO")
	fs.StringVar(&scaleStr, "scale", "", "tier scale overrides, e.g. web=2,db=1")
	fs.BoolVar(&dryRun, "dry-run", false, "parse and validate scenario without launching VMs")
	fs.IntVar(&webPort, "port", 8888, "WebSSH terminal port (0 to disable)")
	fs.Parse(args) //nolint:errcheck

	scenarioFile := fs.Arg(0)
	if scenarioFile == "" {
		fmt.Fprintf(os.Stderr, "error: scenario file is required\n")
		fs.Usage()
		os.Exit(1)
	}

	s, err := scenario.Load(scenarioFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading scenario: %v\n", err)
		os.Exit(1)
	}

	overrides := parseScaleOverrides(scaleStr)
	opts := scenario.RunOptions{
		KernelPath:     cfg.KernelImagePath,
		RootFSPath:     cfg.RootFSPath,
		CloudInitISO:   cfg.CloudInitISO,
		FirecrackerBin: cfg.FirecrackerBin,
		ScaleOverrides: overrides,
		DryRun:         dryRun,
		WebPort:        webPort,
	}

	runner, err := scenario.NewRunner(s, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	runner.WebTerminal = StartWebTerminal

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if err := runner.Run(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "scenario error: %v\n", err)
		os.Exit(1)
	}
}

// parseScaleOverrides parses "web=2,db=1" into map[string]int.
func parseScaleOverrides(s string) map[string]int {
	overrides := make(map[string]int)
	if s == "" {
		return overrides
	}
	for _, part := range strings.Split(s, ",") {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		var n int
		fmt.Sscanf(kv[1], "%d", &n)
		if n > 0 {
			overrides[strings.TrimSpace(kv[0])] = n
		}
	}
	return overrides
}
