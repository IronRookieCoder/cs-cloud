package workflowrunner

import "cs-cloud/internal/workflow"

// Config aliases the workflow package config so callers can use
// workflow.Config directly.
type Config = workflow.Config

// driverState tracks the lifecycle state of the workflow driver.
type driverState int

const (
	driverStateIdle driverState = iota
	driverStateRunning
	driverStateError
)
