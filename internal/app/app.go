package app

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/device"
	"cs-cloud/internal/membertask"
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

func (a *App) memberTaskSecretPath() string {
	return filepath.Join(a.rootDir, "member_task_secret")
}

func (a *App) NewMemberTaskRuntime() (*membertask.Service, string, error) {
	secret, err := a.memberTaskSecret()
	if err != nil {
		return nil, "", err
	}
	store, err := membertask.OpenStore(a.rootDir)
	if err != nil {
		return nil, "", err
	}
	cloud := membertask.NewCloudClient(a.CloudBaseURL(), a.Credentials)
	return membertask.NewService(store, cloud), secret, nil
}

func (a *App) memberTaskSecret() (string, error) {
	if err := a.EnsureRootDir(); err != nil {
		return "", err
	}
	path := a.memberTaskSecretPath()
	for attempt := 0; attempt < 2; attempt++ {
		data, err := os.ReadFile(path)
		if err == nil {
			secret := strings.TrimSpace(string(data))
			if secret == "" {
				return "", fmt.Errorf("member task secret is empty")
			}
			if err := membertask.SecureProfilePermissions(a.rootDir, path); err != nil {
				return "", err
			}
			if err := membertask.ValidateProfilePermissions(a.rootDir, path); err != nil {
				return "", err
			}
			return secret, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("read member task secret: %w", err)
		}
		random := make([]byte, 32)
		if _, err := rand.Read(random); err != nil {
			return "", fmt.Errorf("generate member task secret: %w", err)
		}
		secret := base64.RawURLEncoding.EncodeToString(random)
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create member task secret: %w", err)
		}
		if _, err := file.WriteString(secret + "\n"); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("write member task secret: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return "", fmt.Errorf("sync member task secret: %w", err)
		}
		if err := file.Close(); err != nil {
			return "", fmt.Errorf("close member task secret: %w", err)
		}
	}
	return "", fmt.Errorf("member task secret creation raced repeatedly")
}

func (a *App) MemberTaskSecret() (string, error) {
	return a.memberTaskSecret()
}

func (a *App) NewWorkflowDriver() *workflowrunner.Driver {
	deps := &workflowrunner.Dependencies{
		BackendBaseURL: a.cfg.Workflow.BackendBaseURL,
		UserBaseURL:    a.cfg.Workflow.BackendBaseURL,
		TokenProvider:  a.workflowTokenProvider(),
		AgentEnv:       a.cfg.AgentEnv,
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

func (a *App) PrepareCloudDaemon() (*device.DeviceInfo, error) {
	info, err := a.Device()
	if err != nil || info == nil || strings.TrimSpace(info.DeviceID) == "" || strings.TrimSpace(info.DeviceToken) == "" {
		a.ClearDaemonReadiness()
		return nil, &membertask.TaskError{Code: "device_registration_required", Message: "device registration is required; run cs-cloud register first"}
	}
	if err := device.ValidateDeviceOwner(info); err != nil {
		a.ClearDaemonReadiness()
		return nil, &membertask.TaskError{Code: "device_registration_required", Message: "device registration does not belong to the current user; run cs-cloud register first"}
	}
	return info, nil
}
