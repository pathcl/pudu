package vm

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	firecracker "github.com/firecracker-microvm/firecracker-go-sdk"
	models "github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	log "github.com/sirupsen/logrus"
)

// Machine wraps a firecracker.Machine with its config.
type Machine struct {
	fc      *firecracker.Machine
	cfg     Config
	logFile *os.File
}

// New creates a new Machine from the given Config.
// It satisfies the Factory type signature.
func New(ctx context.Context, cfg Config) (VM, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	// Build drives: rootfs + optional cloud-init ISO
	drives := firecracker.NewDrivesBuilder(cfg.RootFSPath).Build()
	if cfg.CloudInitISO != "" {
		// Add cloud-init ISO as secondary drive
		// DriveID must be alphanumeric + underscores only (no hyphens or dots)
		drives = append(drives, models.Drive{
			DriveID:      firecracker.String("cloudinit"),
			PathOnHost:   firecracker.String(cfg.CloudInitISO),
			IsReadOnly:   firecracker.Bool(true),
			IsRootDevice: firecracker.Bool(false),
		})
	}

	// Remove stale socket from a previous run
	os.Remove(cfg.SocketPath) //nolint:errcheck

	fcCfg := firecracker.Config{
		SocketPath:      cfg.SocketPath,
		KernelImagePath: cfg.KernelImagePath,
		KernelArgs:      cfg.KernelArgs,
		Drives:          drives,
		LogLevel:        cfg.LogLevel,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  firecracker.Int64(cfg.VCPUs),
			MemSizeMib: firecracker.Int64(cfg.MemSizeMiB),
			Smt:        firecracker.Bool(false),
		},
	}

	if cfg.TapDeviceName != "" {
		mac := cfg.MacAddress
		if mac == "" {
			mac = "AA:FC:00:00:00:01"
		}
		fcCfg.NetworkInterfaces = []firecracker.NetworkInterface{{
			StaticConfiguration: &firecracker.StaticNetworkConfiguration{
				MacAddress:  mac,
				HostDevName: cfg.TapDeviceName,
			},
		}}
	}

	// Set up stdio handling based on log file configuration
	var stdin io.Reader = os.Stdin
	var stdout io.Writer = os.Stdout
	var stderr io.Writer = os.Stderr
	var logFile *os.File

	if cfg.LogPath != "" {
		var err error
		logFile, err = os.Create(cfg.LogPath)
		if err != nil {
			return nil, fmt.Errorf("failed to open log file %s: %w", cfg.LogPath, err)
		}
		stdin = io.NopCloser(bytes.NewReader([]byte{}))
		stdout = logFile
		stderr = logFile
	}

	cmd := firecracker.VMCommandBuilder{}.
		WithBin(cfg.FirecrackerBin).
		WithSocketPath(cfg.SocketPath).
		WithStdin(stdin).
		WithStdout(stdout).
		WithStderr(stderr).
		Build(ctx)

	logger := log.New()
	if cfg.LogLevel != "" {
		lvl, err := log.ParseLevel(cfg.LogLevel)
		if err == nil {
			logger.SetLevel(lvl)
		}
	}

	m, err := firecracker.NewMachine(ctx, fcCfg,
		firecracker.WithProcessRunner(cmd),
		firecracker.WithLogger(log.NewEntry(logger)),
	)
	if err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("failed to create machine: %w", err)
	}

	return &Machine{fc: m, cfg: cfg, logFile: logFile}, nil
}

// Start boots the microVM.
func (m *Machine) Start(ctx context.Context) error {
	return m.fc.Start(ctx)
}

// Wait blocks until the VMM exits.
func (m *Machine) Wait(ctx context.Context) error {
	return m.fc.Wait(ctx)
}

// Stop shuts down the VMM and removes the socket.
func (m *Machine) Stop() {
	m.fc.StopVMM() //nolint:errcheck
	os.Remove(m.cfg.SocketPath)
	if m.logFile != nil {
		m.logFile.Close()
	}
}
