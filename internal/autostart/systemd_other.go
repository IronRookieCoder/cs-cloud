//go:build !linux

// Package-level stub: on non-Linux platforms the systemd branch in
// newManager is unreachable at runtime (guarded by runtime.GOOS == "linux"),
// but Go still requires the symbol to be declared for type-checking.
// This file provides an unreachable-only stub that panics if ever invoked.

package autostart

// newSystemdUserManager is unreachable on non-Linux because newManager
// only routes here when runtime.GOOS == "linux". The stub exists so the
// package compiles on darwin/windows without a systemd implementation.
func newSystemdUserManager(cfg Config) Manager {
	return &unsupportedManager{cfg: cfg, reason: "systemd-user not available on this OS"}
}

// LingerStatus is unreachable on non-Linux because the CLI only invokes
// it inside a runtime.GOOS == "linux" branch. Returns empty string with
// no error so callers' advise-Linger path is a no-op.
func LingerStatus(_ commandRunner) (string, error) {
	return "", nil
}
