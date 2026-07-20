package workflow

import "cs-cloud/internal/provider"

// Dependencies holds the external dependencies required by the workflow driver.
type Dependencies struct {
	MulticaBaseURL string
	TokenProvider  func() (*provider.Credentials, error)
	// DeviceID resolves the CoStrict Gateway device_id this daemon runs as.
	// It is used as the daemon_id when registering with multica. When nil,
	// daemon registration is disabled (the driver still runs tasks pushed
	// via localserver routes).
	DeviceID func() (string, error)
}
