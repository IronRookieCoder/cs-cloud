package workflow

import "cs-cloud/internal/provider"

// Dependencies holds the external dependencies required by the workflow driver.
type Dependencies struct {
	MulticaBaseURL string
	TokenProvider  func() (*provider.Credentials, error)
}
