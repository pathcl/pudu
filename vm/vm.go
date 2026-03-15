package vm

import "context"

// VM is the interface satisfied by a running Firecracker microVM.
// Using this interface instead of *Machine directly lets callers
// (api, scenario runner) be tested with a FakeVM without root or KVM.
type VM interface {
	Start(ctx context.Context) error
	Wait(ctx context.Context) error
	Stop()
}

// Factory creates a VM from a Config. The default implementation is New.
// Tests inject a FakeVM factory that returns immediately without Firecracker.
type Factory func(ctx context.Context, cfg Config) (VM, error)
