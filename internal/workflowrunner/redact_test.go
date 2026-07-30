package workflowrunner

import (
	"strings"
	"testing"

	"cs-cloud/internal/workflow"
)

func TestRedactURL(t *testing.T) {
	cases := map[string]string{
		"https://oauth2:glpat-abcdef@gitlab.local/root/co.git": "https://oauth2:***@gitlab.local/root/co.git",
		"http://user:p%40ss@gitea:3000/o/r.git":                "http://user:***@gitea:3000/o/r.git",
		"https://gitlab.local/root/co.git":                      "https://gitlab.local/root/co.git",
		"":                                                      "",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEnvKeySummary(t *testing.T) {
	env := map[string]string{
		"CS_CLOUD_GITLAB_TOKEN":          "secret-token",
		"CS_CLOUD_REPO_CLONE_URL_AUTHED": "https://oauth2:x@g/o/r.git",
		"CS_CLOUD_REPO_NODE_BRANCH":      "node-1",
		"PATH":                           "/usr/bin",
	}
	got := envKeySummary(env)
	for _, want := range []string{
		"CS_CLOUD_GITLAB_TOKEN=<redacted>",
		"CS_CLOUD_REPO_CLONE_URL_AUTHED=<redacted>",
		"CS_CLOUD_REPO_NODE_BRANCH",
		"PATH",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("envKeySummary missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, "secret-token") {
		t.Errorf("envKeySummary leaked a secret value: %q", got)
	}
}

func TestRepoSummary(t *testing.T) {
	repos := []workflow.RepoSpec{
		{URL: "https://oauth2:t@host/a/b.git", Provider: "gitlab", Role: "code", Alias: "main"},
		{URL: "https://gitea/o/r.git", Provider: "gitea", Role: "delivery", BaseBranch: "inst-1"},
	}
	got := repoSummary(repos, "")
	if !strings.Contains(got, "code:gitlab:https://oauth2:***@host/a/b.git(alias=main)") {
		t.Errorf("code repo not redacted/summarized: %q", got)
	}
	if !strings.Contains(got, "delivery:gitea:https://gitea/o/r.git(base=inst-1)") {
		t.Errorf("delivery repo not summarized: %q", got)
	}
}

func TestDeliverableSummary(t *testing.T) {
	empty := deliverableSummary(nil)
	if empty != "[]" {
		t.Errorf("empty = %q, want []", empty)
	}
	withRepo := deliverableSummary([]workflow.DeliverableSpec{
		{ID: "d1", RepoAlias: "code"},
	})
	if !strings.Contains(withRepo, "d1(repo=code)") {
		t.Errorf("with repo alias = %q, want d1(repo=code)", withRepo)
	}
	noAlias := deliverableSummary([]workflow.DeliverableSpec{
		{ID: "d2"},
	})
	if !strings.Contains(noAlias, "d2") {
		t.Errorf("no alias = %q, want d2", noAlias)
	}
}

func TestPluginName(t *testing.T) {
	if got := pluginName(nil); got != "none" {
		t.Errorf("pluginName(nil) = %q, want none", got)
	}
	if got := pluginName(&workflow.PluginSpec{Install: &workflow.PluginInstallSpec{PluginName: "superpowers"}}); got != "superpowers" {
		t.Errorf("pluginName = %q, want superpowers", got)
	}
	if got := pluginName(&workflow.PluginSpec{Name: "fallback", Install: &workflow.PluginInstallSpec{}}); got != "fallback" {
		t.Errorf("pluginName fallback = %q, want fallback", got)
	}
}
