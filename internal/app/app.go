package app

import (
	"os"
	"strings"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/device"
	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflowrunner"
)

type App struct {
	rootDir string
	cfg     *config.Config
}

func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	return &App{rootDir: platform.AppDir(), cfg: cfg}, nil
}

func (a *App) RootDir() string        { return a.rootDir }
func (a *App) Config() *config.Config { return a.cfg }

func (a *App) EnsureRootDir() error {
	return os.MkdirAll(a.rootDir, 0o755)
}

func (a *App) CloudBaseURL() string {
	client := device.NewClient(a.cfg)
	return client.CloudBaseURL()
}

func (a *App) OIDCBaseURL(credBaseURL string) string {
	cc := cloud.NewClient(a.cfg)
	return cc.OIDCBaseURL(credBaseURL)
}

func (a *App) Credentials() (*provider.Credentials, error) {
	return provider.LoadCredentials()
}

func (a *App) NewWorkflowDriver() *workflowrunner.Driver {
	deps := &workflowrunner.Dependencies{
		BackendBaseURL: a.cfg.Workflow.BackendBaseURL,
		UserBaseURL:    a.cfg.Workflow.BackendBaseURL,
		TokenProvider:  a.workflowTokenProvider(),
		DeviceID: func() (string, error) {
			dev, err := a.Device()
			if err != nil {
				return "", err
			}
			if dev == nil {
				return "", nil
			}
			return dev.DeviceID, nil
		},
	}
	return workflowrunner.NewDriver(a.cfg.Workflow, deps)
}

// workflowTokenProvider returns the credentials the workflow runner uses to
// register with (and run in-task CLIs against) the workflow backend. By default
// it shares the cloud auth.json token (Credentials). When
// CS_CLOUD_WORKFLOW_BACKEND_TOKEN is set, it overrides with that token — letting
// the workflow backend point at a DIFFERENT deployment than the cloud gateway
// (e.g. cloud tunnel → zgsmtest while workflow backend → a local multica with
// its own PAT). The override is provider.Credentials-shaped so the workflow
// Client's Bearer auth works unchanged.
func (a *App) workflowTokenProvider() func() (*provider.Credentials, error) {
	if tok := strings.TrimSpace(os.Getenv("CS_CLOUD_WORKFLOW_BACKEND_TOKEN")); tok != "" {
		return func() (*provider.Credentials, error) {
			return &provider.Credentials{AccessToken: tok}, nil
		}
	}
	return a.Credentials
}

func (a *App) Device() (*device.DeviceInfo, error) {
	return device.LoadDevice()
}
