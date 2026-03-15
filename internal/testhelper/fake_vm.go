// Package testhelper provides shared test utilities for pudu packages.
// It must not be imported by non-test code.
package testhelper

import (
	"context"

	"github.com/pathcl/pudu/vm"
)

// FakeVM is a no-op implementation of vm.VM for use in tests.
// It returns immediately from Start/Wait without launching Firecracker.
type FakeVM struct {
	StartErr error
	WaitErr  error
	Stopped  bool
}

func (f *FakeVM) Start(_ context.Context) error { return f.StartErr }
func (f *FakeVM) Wait(_ context.Context) error  { return f.WaitErr }
func (f *FakeVM) Stop()                         { f.Stopped = true }

// NewFakeFactory returns a vm.Factory that creates FakeVMs.
// If errOn contains a VM id, that VM returns StartErr.
func NewFakeFactory() vm.Factory {
	return func(_ context.Context, _ vm.Config) (vm.VM, error) {
		return &FakeVM{}, nil
	}
}
