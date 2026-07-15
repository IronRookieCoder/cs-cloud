package workflow

import (
	"cs-cloud/internal/provider"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/workflow"
)

// Compile-time check that Driver implements runtime.PersistentDriver.
var _ runtime.PersistentDriver = (*Driver)(nil)

// Driver is a persistent driver for the cs-workflow subsystem.
type Driver struct {
	cfg  workflow.Config
	deps *Dependencies
}

// NewDriver creates a new workflow driver.
func NewDriver(cfg workflow.Config, deps *Dependencies) *Driver {
	return &Driver{cfg: cfg, deps: deps}
}

// Name returns the driver name.
func (d *Driver) Name() string { return "workflow" }

// Start is a no-op skeleton for Phase 2.
func (d *Driver) Start() error { return nil }

// Stop is a no-op skeleton for Phase 2.
func (d *Driver) Stop() error { return nil }

// Health is a no-op skeleton for Phase 2.
func (d *Driver) Health() error { return nil }

// tokenProvider returns the configured credential provider, or nil if deps
// have not been supplied.
func (d *Driver) tokenProvider() func() (*provider.Credentials, error) {
	if d.deps == nil {
		return nil
	}
	return d.deps.TokenProvider
}
