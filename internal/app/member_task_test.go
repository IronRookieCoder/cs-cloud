package app

import (
	"os"
	"testing"

	"cs-cloud/internal/config"
	"cs-cloud/internal/membertask"
	"cs-cloud/internal/workflow"
)

func TestMemberTaskRuntimeCreatesStableSecuredSecret(t *testing.T) {
	a := &App{rootDir: t.TempDir(), cfg: &config.Config{CloudBaseURL: "https://cloud.invalid", Workflow: workflow.Config{BackendBaseURL: "https://workflow.invalid/"}}}
	service, first, err := a.NewMemberTaskRuntime()
	if err != nil {
		t.Fatalf("NewMemberTaskRuntime: %v", err)
	}
	if service == nil || first == "" {
		t.Fatalf("service=%v secret=%q", service, first)
	}
	_, second, err := a.NewMemberTaskRuntime()
	if err != nil {
		t.Fatalf("second NewMemberTaskRuntime: %v", err)
	}
	if first != second {
		t.Fatalf("secret changed between calls")
	}
	if err := membertask.ValidateProfilePermissions(a.RootDir(), a.memberTaskSecretPath()); err != nil {
		t.Fatalf("profile permissions: %v", err)
	}
}

func TestMemberTaskRuntimeUsesWorkflowBackendURL(t *testing.T) {
	a := &App{rootDir: t.TempDir(), cfg: &config.Config{
		CloudBaseURL: "https://cloud.invalid/cloud-api",
		Workflow:     workflow.Config{BackendBaseURL: "https://workflow.invalid/workflow-backend/"},
	}}
	service, _, err := a.NewMemberTaskRuntime()
	if err != nil || service == nil {
		t.Fatalf("NewMemberTaskRuntime: service=%v err=%v", service, err)
	}
}

func TestMemberTaskRuntimeRejectsMalformedSecret(t *testing.T) {
	a := &App{rootDir: t.TempDir(), cfg: &config.Config{CloudBaseURL: "https://cloud.invalid"}}
	if err := os.WriteFile(a.memberTaskSecretPath(), []byte("c2hvcnQ\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.NewMemberTaskRuntime(); err == nil {
		t.Fatal("NewMemberTaskRuntime accepted a secret shorter than 32 bytes")
	}
}
