package workflowrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cs-cloud/internal/workflow"
)

func TestNormalizeCloudSkillInstalls(t *testing.T) {
	mk := func(id, slug, method, target string) workflow.CloudSkillInstall {
		return workflow.CloudSkillInstall{
			ID: id, Slug: slug,
			Install: &workflow.CloudSkillInstallSpec{Method: method, Spec: target},
		}
	}

	t.Run("slug target ok", func(t *testing.T) {
		got, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{
			mk("a", "my-skill", "csc", "my-skill"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].target != "my-skill" {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("canonical UUID target ok", func(t *testing.T) {
		u := "12345678-1234-1234-1234-1234567890ab"
		got, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{
			{ID: u, Install: &workflow.CloudSkillInstallSpec{SkillID: u}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].target != u {
			t.Fatalf("got %q", got[0].target)
		}
	})

	t.Run("uuid metadata wins over slug", func(t *testing.T) {
		u := "12345678-1234-1234-1234-1234567890ab"
		got, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{
			{
				ID:   u,
				Slug: "code-review",
				Install: &workflow.CloudSkillInstallSpec{
					Method:  "csc",
					Spec:    u,
					SkillID: u,
				},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got[0].target != u {
			t.Fatalf("got %q", got[0].target)
		}
	})

	t.Run("unsupported method rejected", func(t *testing.T) {
		if _, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{
			mk("a", "s", "npm", "s"),
		}); err == nil {
			t.Fatal("expected error for unsupported method")
		}
	})

	t.Run("slug/target mismatch rejected", func(t *testing.T) {
		if _, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{
			mk("a", "slug-one", "csc", "different"),
		}); err == nil {
			t.Fatal("expected error when target != slug")
		}
	})

	t.Run("duplicate target rejected", func(t *testing.T) {
		if _, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{
			mk("a", "s", "csc", "s"), mk("b", "s", "csc", "s"),
		}); err == nil {
			t.Fatal("expected duplicate error")
		}
	})

	t.Run("too many rejected", func(t *testing.T) {
		tooMany := make([]workflow.CloudSkillInstall, 0, maxCloudSkillCount+1)
		for i := 0; i <= maxCloudSkillCount; i++ {
			tooMany = append(tooMany, mk("a"+itoa(i), "s"+itoa(i), "csc", "s"+itoa(i)))
		}
		if _, err := normalizeCloudSkillInstalls(tooMany); err == nil {
			t.Fatal("expected too-many error")
		}
	})

	t.Run("missing install metadata rejected", func(t *testing.T) {
		if _, err := normalizeCloudSkillInstalls([]workflow.CloudSkillInstall{{ID: "a"}}); err == nil {
			t.Fatal("expected missing-install error")
		}
	})
}

func itoa(i int) string {
	// tiny strconv.Itoa-free helper to keep the test self-contained
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// fakeCSC compiles a tiny recorder binary that appends each invocation's
// "cwd\x1fargv-joined" line to $CSC_RECORD_FILE, so tests can assert the exact
// csc subcommands and that cmd.Dir was set.
func fakeCSC(t *testing.T) (bin string, recordFile string) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not on PATH; cannot build fake csc")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	const srcCode = `package main

import (
	"os"
	"strings"
)

func main() {
	rec := os.Getenv("CSC_RECORD_FILE")
	wd, _ := os.Getwd()
	if rec != "" {
		f, err := os.OpenFile(rec, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			f.WriteString(wd + "\x1f" + strings.Join(os.Args[1:], "\x1f") + "\n")
			for _, key := range strings.Split(os.Getenv("CSC_RECORD_ENV_KEYS"), ",") {
				if key != "" {
					f.WriteString("env\x1f" + key + "\x1f" + os.Getenv(key) + "\n")
				}
			}
			f.Close()
		}
	}
	if out := os.Getenv("CSC_STDOUT"); out != "" {
		os.Stdout.WriteString(out)
	}
	if errOut := os.Getenv("CSC_STDERR"); errOut != "" {
		os.Stderr.WriteString(errOut)
	}
	if os.Getenv("CSC_FAIL") == "1" {
		os.Exit(1)
	}
}
`
	if err := os.WriteFile(src, []byte(srcCode), 0o644); err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	bin = filepath.Join(dir, "csc"+suffix)
	build := exec.Command("go", "build", "-o", bin, src)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake csc: %v\n%s", err, out)
	}
	recordFile = filepath.Join(t.TempDir(), "calls.log")
	return bin, recordFile
}

func TestInstallCSCAddons_PluginCommands(t *testing.T) {
	bin, record := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_RECORD_FILE", record)

	payload := workflow.TaskRunPayload{
		Plugin: &workflow.PluginSpec{
			Install: &workflow.PluginInstallSpec{
				PluginName:      "superpowers",
				MarketplaceName: "costrict-plugins",
				MarketplaceRepo: "https://example.test/marketplace.git",
			},
		},
	}
	if err := installCSCAddons(context.Background(), bin, workDir, payload, nil); err != nil {
		t.Fatalf("installCSCAddons: %v", err)
	}

	data, _ := os.ReadFile(record)
	calls := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	wantSubstrings := []string{
		"plugin\x1fmarketplace\x1fadd\x1fhttps://example.test/marketplace.git",
		"plugin\x1fmarketplace\x1fupdate\x1fcostrict-plugins",
		"plugin\x1finstall\x1fsuperpowers@costrict-plugins\x1f-s\x1flocal",
		"plugin\x1fupdate\x1fsuperpowers@costrict-plugins\x1f-s\x1flocal",
	}
	if len(calls) != len(wantSubstrings) {
		t.Fatalf("expected %d csc calls, got %d: %q", len(wantSubstrings), len(calls), calls)
	}
	for i, want := range wantSubstrings {
		if !strings.HasSuffix(calls[i], want) {
			t.Errorf("call %d = %q\n  want suffix %q", i, calls[i], want)
		}
		if !strings.HasPrefix(calls[i], workDir+"\x1f") {
			t.Errorf("call %d did not run with cmd.Dir=workdir: %q", i, calls[i])
		}
	}
}

func TestRunCSCCmdTreatsSuccessOutputWithUVAssertionAsSuccess(t *testing.T) {
	bin, _ := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_FAIL", "1")
	t.Setenv("CSC_STDOUT", "正在更新市场: costrict-plugins...\n√ 成功更新市场: costrict-plugins\n")
	t.Setenv("CSC_STDERR", "Assertion failed: !(handle->flags & UV_HANDLE_CLOSING), file src\\win\\async.c, line 76")

	err := runCSCCmd(context.Background(), bin, workDir, nil, "plugin", "marketplace", "update", "costrict-plugins")
	if err != nil {
		t.Fatalf("expected success output to win over post-command UV assertion, got %v", err)
	}
}

func TestInstallCSCAddons_SkillCommands(t *testing.T) {
	bin, record := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_RECORD_FILE", record)

	payload := workflow.TaskRunPayload{
		CloudSkills: []workflow.CloudSkillInstall{
			{ID: "id-1", Slug: "code-review", Install: &workflow.CloudSkillInstallSpec{Method: "csc", Spec: "code-review"}},
			{ID: "id-2", Slug: "plan", Install: &workflow.CloudSkillInstallSpec{Method: "csc", Spec: "plan"}},
		},
	}
	if err := installCSCAddons(context.Background(), bin, workDir, payload, nil); err != nil {
		t.Fatalf("installCSCAddons: %v", err)
	}

	data, _ := os.ReadFile(record)
	calls := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(calls) != 2 {
		t.Fatalf("expected 2 skill calls, got %d: %q", len(calls), calls)
	}
	for i, target := range []string{"code-review", "plan"} {
		want := "skill\x1finstall\x1f" + target + "\x1f--scope\x1fproject\x1f--force\x1f--json"
		if !strings.HasSuffix(calls[i], want) {
			t.Errorf("skill call %d = %q\n  want suffix %q", i, calls[i], want)
		}
		if !strings.HasPrefix(calls[i], workDir+"\x1f") {
			t.Errorf("skill call %d did not run with cmd.Dir=workdir: %q", i, calls[i])
		}
	}
}

func TestInstallCSCAddons_PassesAgentEnvToSkillInstall(t *testing.T) {
	bin, record := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_RECORD_FILE", record)
	t.Setenv("CSC_RECORD_ENV_KEYS", "COSTRICT_BASE_URL")

	payload := workflow.TaskRunPayload{
		CloudSkills: []workflow.CloudSkillInstall{
			{
				ID:   "83c97a47-dee8-4cf7-afd3-5153673f17d9",
				Slug: "code-review",
				Install: &workflow.CloudSkillInstallSpec{
					Method:  "csc",
					SkillID: "83c97a47-dee8-4cf7-afd3-5153673f17d9",
				},
			},
		},
	}
	env := setEnv(os.Environ(), "COSTRICT_BASE_URL", "https://catalog.example.test")
	if err := installCSCAddons(context.Background(), bin, workDir, payload, env); err != nil {
		t.Fatalf("installCSCAddons: %v", err)
	}

	data, _ := os.ReadFile(record)
	if !strings.Contains(string(data), "env\x1fCOSTRICT_BASE_URL\x1fhttps://catalog.example.test") {
		t.Fatalf("install did not receive COSTRICT_BASE_URL env:\n%s", data)
	}
}

func TestInstallCSCAddons_NoopWhenEmpty(t *testing.T) {
	bin, record := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_RECORD_FILE", record)

	// no plugin, no skills → no calls, no error
	if err := installCSCAddons(context.Background(), bin, workDir, workflow.TaskRunPayload{}, nil); err != nil {
		t.Fatalf("installCSCAddons: %v", err)
	}
	if _, err := os.Stat(record); err == nil {
		data, _ := os.ReadFile(record)
		t.Errorf("expected no csc invocations, got %q", string(data))
	}
}

func TestInstallCSCAddons_PluginInstallFailureFails(t *testing.T) {
	bin, record := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_RECORD_FILE", record)
	// marketplace add is non-fatal, but marketplace update must fail → whole install fails.
	t.Setenv("CSC_FAIL", "1")

	payload := workflow.TaskRunPayload{
		Plugin: &workflow.PluginSpec{
			Install: &workflow.PluginInstallSpec{PluginName: "p", MarketplaceName: "mp", MarketplaceRepo: "https://x/y.git"},
		},
	}
	if err := installCSCAddons(context.Background(), bin, workDir, payload, nil); err == nil {
		t.Fatal("expected install to fail when csc exits non-zero on a fatal step")
	}
}

func TestInstallCloudSkillFailureIncludesStdoutAndStderr(t *testing.T) {
	bin, _ := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_FAIL", "1")
	t.Setenv("CSC_STDOUT", "remote skill not found")
	t.Setenv("CSC_STDERR", "Assertion failed: !(handle->flags & UV_HANDLE_CLOSING)")

	err := installCloudSkill(context.Background(), bin, workDir, normalizedCloudSkillInstall{
		id:     "skill-id",
		target: "code-review",
	}, nil)
	if err == nil {
		t.Fatal("expected install failure")
	}
	msg := err.Error()
	if !strings.Contains(msg, "remote skill not found") || !strings.Contains(msg, "UV_HANDLE_CLOSING") {
		t.Fatalf("error missing combined output: %v", err)
	}
}

func TestInstallCloudSkillTreatsSuccessJSONWithUVAssertionAsInstalled(t *testing.T) {
	bin, _ := fakeCSC(t)
	workDir := t.TempDir()
	t.Setenv("CSC_FAIL", "1")
	t.Setenv("CSC_STDOUT", `{"id":"e5aaa64b-b4af-4b5a-a058-39bdce045e63","slug":"cheat-publish-skill","name":"cheat-publish","targetName":"cheat-publish-skill","path":"C:\\Users\\SXF-Admin\\.costrict\\skills\\cheat-publish-skill\\SKILL.md","scope":"project"}`)
	t.Setenv("CSC_STDERR", "Assertion failed: !(handle->flags & UV_HANDLE_CLOSING), file src\\win\\async.c, line 76")

	err := installCloudSkill(context.Background(), bin, workDir, normalizedCloudSkillInstall{
		id:     "e5aaa64b-b4af-4b5a-a058-39bdce045e63",
		target: "e5aaa64b-b4af-4b5a-a058-39bdce045e63",
	}, nil)
	if err != nil {
		t.Fatalf("expected success JSON to win over post-install UV assertion, got %v", err)
	}
}
