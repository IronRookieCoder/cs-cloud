package workflowrunner

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/workflow"
)

// cs-cloud ports multica's local-daemon plugin/cloud-skill install path so a
// cs-cloud 数智人 gets its configured Plugin and CloudSkills materialized in the
// task working directory before the csc session runs. csc resolves plugins
// (-s local) and skills (--scope project) by cwd, and the bound session's cwd
// is the same task workdir (driver.bindSession → agent.createSessionWithEnv),
// so installing here makes them visible to the session — identical to how
// multica's execenv.Prepare does it for the local daemon.

const (
	cscCmdTimeout        = 120 * time.Second
	cscSkillCmdTimeout   = 120 * time.Second
	cscSkillOutputLimit  = 4 * 1024
	maxCloudSkillCount   = 20
	maxCloudSkillTarget  = 200
)

// Built-in github defaults for the CSC plugin marketplace. Used only when the
// server does not deliver a marketplace identity, matching multica's
// defaultCSCMarketplaceName/Repo (execenv/plugins.go).
const (
	defaultCSCMarketplaceName = "costrict-plugins"
	defaultCSCMarketplaceRepo = "https://github.com/costrict-plugins-repo/marketplace.git"
)

var (
	safeCloudSkillSlug    = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	canonicalCloudSkillID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// installCSCAddons installs the agent's plugin and cloud skills into the task
// workdir. It is a no-op when the payload carries neither. Failures are
// fail-closed (returned) — like multica, a configured plugin/skill that cannot
// install means the task cannot run meaningfully.
func installCSCAddons(ctx context.Context, cscBin, workDir string, payload workflow.TaskRunPayload) error {
	if cscBin == "" {
		// Nothing to install with; the caller (Driver.execute) only reaches here
		// after Prepare resolved the csc binary, so this is defensive.
		if payload.Plugin == nil && len(payload.CloudSkills) == 0 {
			return nil
		}
		return fmt.Errorf("csc addon install requires a csc binary")
	}
	if payload.Plugin != nil {
		if err := setupCSCPlugins(ctx, cscBin, workDir, payload.Plugin); err != nil {
			return err
		}
	}
	if len(payload.CloudSkills) > 0 {
		if err := setupCSCSkills(ctx, cscBin, workDir, payload.CloudSkills); err != nil {
			return err
		}
	}
	return nil
}

// setupCSCPlugins installs one CSC plugin into the task's working directory:
//
//  1. csc plugin marketplace add <marketplaceRepo>        (non-fatal)
//  2. csc plugin marketplace update <marketplaceName>
//  3. csc plugin install <pluginName>@<marketplaceName> -s local
//  4. csc plugin update <pluginName>@<marketplaceName> -s local
//
// All commands run with cmd.Dir = workDir (CSC uses cwd + scope, not --dir).
// marketplace add failure is non-fatal: the marketplace may already be registered.
func setupCSCPlugins(ctx context.Context, cscBin, workDir string, plugin *workflow.PluginSpec) error {
	if plugin == nil || plugin.Install == nil {
		return nil
	}
	install := plugin.Install

	name := install.MarketplaceName
	if name == "" {
		name = defaultCSCMarketplaceName
	}
	repo := install.MarketplaceRepo
	if repo == "" {
		repo = defaultCSCMarketplaceRepo
	}

	logger.Info("workflow: installing csc plugin: plugin=%s marketplace=%s repo=%s workdir=%s",
		install.PluginName, name, repo, workDir)

	// Step 1: marketplace add (non-fatal — may already be registered).
	if err := runCSCCmd(ctx, cscBin, workDir, "plugin", "marketplace", "add", repo); err != nil {
		logger.Warn("workflow: csc plugin marketplace add failed (non-fatal): repo=%s err=%v", repo, err)
	}

	// Step 2: marketplace update.
	if err := runCSCCmd(ctx, cscBin, workDir, "plugin", "marketplace", "update", name); err != nil {
		return fmt.Errorf("csc plugin marketplace update %s: %w", name, err)
	}

	// Step 3: install with local scope.
	spec := install.PluginName + "@" + name
	if err := runCSCCmd(ctx, cscBin, workDir, "plugin", "install", spec, "-s", "local"); err != nil {
		return fmt.Errorf("csc plugin install %s: %w", spec, err)
	}

	// Step 4: update installed plugin.
	if err := runCSCCmd(ctx, cscBin, workDir, "plugin", "update", spec, "-s", "local"); err != nil {
		return fmt.Errorf("csc plugin update %s: %w", spec, err)
	}

	logger.Info("workflow: csc plugin installed: plugin=%s marketplace=%s", install.PluginName, name)
	return nil
}

// setupCSCSkills installs each cloud skill binding via
// `csc skill install <target> --scope project --force --json` in the task workdir.
func setupCSCSkills(ctx context.Context, cscBin, workDir string, skills []workflow.CloudSkillInstall) error {
	installs, err := normalizeCloudSkillInstalls(skills)
	if err != nil {
		return err
	}
	for _, install := range installs {
		if err := installCloudSkill(ctx, cscBin, workDir, install); err != nil {
			return err
		}
	}
	return nil
}

// runCSCCmd executes a csc CLI command with cmd.Dir = workDir and a bounded
// timeout. Captures stdout+stderr into the returned error for diagnostics.
// Ported from multica's execenv.runCSCCmd.
func runCSCCmd(ctx context.Context, cscBin, workDir string, args ...string) error {
	cmdCtx, cancel := context.WithTimeout(ctx, cscCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, cscBin, args...)
	cmd.Dir = workDir
	var stdout strings.Builder
	var stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(strings.Join([]string{
			strings.TrimSpace(stdout.String()),
			strings.TrimSpace(stderr.String()),
		}, "\n"))
		if output != "" {
			return fmt.Errorf("%w: %s", err, output)
		}
		return err
	}
	return nil
}

type normalizedCloudSkillInstall struct {
	id     string
	target string
}

// normalizeCloudSkillInstalls validates and de-duplicates cloud skill bindings,
// returning the executable install target for each. Ported from multica's
// execenv.normalizeCloudSkillInstalls (UUID validation done via regex to avoid
// pulling google/uuid into cs-cloud).
func normalizeCloudSkillInstalls(bindings []workflow.CloudSkillInstall) ([]normalizedCloudSkillInstall, error) {
	if len(bindings) > maxCloudSkillCount {
		return nil, fmt.Errorf("cloud skill bindings must contain at most %d items", maxCloudSkillCount)
	}
	normalized := make([]normalizedCloudSkillInstall, 0, len(bindings))
	seen := make(map[string]struct{}, len(bindings))
	for i, binding := range bindings {
		id := strings.TrimSpace(binding.ID)
		if binding.Install == nil {
			return nil, fmt.Errorf("cloud skill %q at position %d has missing install metadata", id, i)
		}
		method := strings.TrimSpace(binding.Install.Method)
		switch method {
		case "", "csc", "csc_skill":
			// ok
		default:
			return nil, fmt.Errorf("cloud skill %q at position %d has unsupported install method %q", id, i, method)
		}

		target := firstNonEmptyTrimmed(binding.Install.Spec, binding.Install.SkillID, binding.ID)
		if target == "" {
			return nil, fmt.Errorf("cloud skill at position %d has an empty install target", i)
		}
		if canonicalCloudSkillID.MatchString(target) {
			// canonical UUID target — accepted as-is
		} else {
			slug := strings.TrimSpace(binding.Slug)
			if target != slug || len(target) > maxCloudSkillTarget || strings.HasPrefix(target, "-") || !safeCloudSkillSlug.MatchString(target) {
				return nil, fmt.Errorf("cloud skill %q at position %d has an invalid slug target %q", id, i, target)
			}
		}
		if _, duplicate := seen[target]; duplicate {
			return nil, fmt.Errorf("cloud skill %q at position %d duplicates install target %q", id, i, target)
		}
		seen[target] = struct{}{}
		normalized = append(normalized, normalizedCloudSkillInstall{id: id, target: target})
	}
	return normalized, nil
}

func firstNonEmptyTrimmed(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// installCloudSkill runs `csc skill install <target> --scope project --force --json`
// in the task workdir with bounded output capture. Ported from multica's
// execenv.installCloudSkill.
func installCloudSkill(ctx context.Context, cscBin, workDir string, install normalizedCloudSkillInstall) error {
	logger.Info("workflow: installing csc skill: target=%s id=%s workdir=%s", install.target, install.id, workDir)

	installCtx, cancel := context.WithTimeout(ctx, cscSkillCmdTimeout)
	defer cancel()
	cmd := exec.CommandContext(installCtx, cscBin,
		"skill", "install", install.target, "--scope", "project", "--force", "--json")
	cmd.Dir = workDir
	stdout := &boundedOutput{limit: cscSkillOutputLimit}
	stderr := &boundedOutput{limit: cscSkillOutputLimit}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err := cmd.Run()
	if err == nil {
		logger.Info("workflow: csc skill installed: target=%s id=%s", install.target, install.id)
		return nil
	}
	if ctxErr := installCtx.Err(); ctxErr != nil {
		err = ctxErr
	}
	detail := strings.TrimSpace(stderr.String())
	if detail != "" {
		detail = ": " + detail
	}
	logger.Error("workflow: csc skill install failed: target=%s id=%s err=%v", install.target, install.id, err)
	return fmt.Errorf("csc cloud skill install %q (id %q) failed%s: %w", install.target, install.id, detail, err)
}

// boundedOutput is a bytes.Buffer that caps captured output at limit bytes and
// marks truncation, so a verbose csc cannot blow up memory or logs.
type boundedOutput struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
		} else {
			_, _ = b.buf.Write(p)
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	return written, nil
}

func (b *boundedOutput) String() string {
	if !b.truncated {
		return b.buf.String()
	}
	return b.buf.String() + "\n...[truncated]"
}
