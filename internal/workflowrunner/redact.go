package workflowrunner

import (
	"regexp"
	"strings"

	"cs-cloud/internal/workflow"
)

// urlCredRedactor matches scheme://user:pass@host so log lines never leak an
// embedded PAT/oauth2 token. Mirrors internal/cli/gitea.go:urlCredRedactor.
var urlCredRedactor = regexp.MustCompile(`(\w+://[^/:@]+:)[^@]+(@)`)

// redactURL strips embedded credentials (oauth2:token@, user:pass@) from a URL
// for safe logging.
func redactURL(u string) string {
	if u == "" {
		return ""
	}
	return urlCredRedactor.ReplaceAllString(u, "${1}***${2}")
}

// credEnvToken matches environment-variable names that carry secrets — their
// values are never logged, only that the key was present.
var credEnvToken = regexp.MustCompile(`(?i)(TOKEN|PAT|SECRET|PASSWORD|CREDENTIAL|AUTHED)`)

// envKeySummary renders the env injected into a task as a log-safe list of key
// names. Credential-bearing keys are marked =<redacted>; non-secret keys are
// shown by name only (values omitted) so the line stays compact and safe.
func envKeySummary(env map[string]string) string {
	if len(env) == 0 {
		return "[]"
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		if credEnvToken.MatchString(k) {
			keys = append(keys, k+"=<redacted>")
		} else {
			keys = append(keys, k)
		}
	}
	return "[" + strings.Join(keys, ", ") + "]"
}

// repoSummary renders the repos dispatched to a task as a compact, log-safe
// list: role:provider:<redacted-url>(alias=…,base=…).
func repoSummary(repos []workflow.RepoSpec, legacyRepoURL string) string {
	parts := make([]string, 0, len(repos)+1)
	for _, r := range repos {
		var b strings.Builder
		b.WriteString(r.Role)
		b.WriteString(":")
		b.WriteString(r.Provider)
		b.WriteString(":")
		b.WriteString(redactURL(r.URL))
		extras := make([]string, 0, 2)
		if r.Alias != "" {
			extras = append(extras, "alias="+r.Alias)
		}
		if r.BaseBranch != "" {
			extras = append(extras, "base="+r.BaseBranch)
		}
		if len(extras) > 0 {
			b.WriteString("(" + strings.Join(extras, ",") + ")")
		}
		parts = append(parts, b.String())
	}
	if legacyRepoURL != "" {
		parts = append(parts, "code(legacy):"+redactURL(legacyRepoURL))
	}
	if len(parts) == 0 {
		return "[]"
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// deliverableSummary renders the deliverable contracts for a task:
// [id(repo=alias); …].
func deliverableSummary(ds []workflow.DeliverableSpec) string {
	if len(ds) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		s := d.ID
		if d.RepoAlias != "" {
			s += "(repo=" + d.RepoAlias + ")"
		}
		parts = append(parts, s)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

// pluginName returns the install plugin name for logging, or "none".
func pluginName(p *workflow.PluginSpec) string {
	if p == nil || p.Install == nil {
		return "none"
	}
	if p.Install.PluginName != "" {
		return p.Install.PluginName
	}
	return p.Name
}
