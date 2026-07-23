package workflow

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// installFakeAgent creates an executable named name in a temporary directory and
// prepends that directory to PATH so tests can drive TaskRunner without relying
// on a real agent CLI. The fake agent echoes its first argument (so callers can
// verify the prompt was passed as a positional argument) or sleeps when the
// prompt starts with "sleep ".
func installFakeAgent(t *testing.T, name string) {
	t.Helper()

	dir := t.TempDir()
	var bin string
	if runtime.GOOS == "windows" {
		script := `@echo off
if not "%FAKE_AGENT_STARTED_FILE%"=="" <nul set /p=started>"%FAKE_AGENT_STARTED_FILE%"
if not "%FAKE_AGENT_PRINT_ENV%"=="" call echo %%%FAKE_AGENT_PRINT_ENV%%%
set first=%~1
if /I "%first:~0,6%"=="sleep " powershell -NoProfile -Command "Start-Sleep -Seconds ([int]'%first:~6%')" & exit /b 0
echo %*
`
		bin = filepath.Join(dir, name+".cmd")
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	} else {
		script := `#!/bin/sh
if [ -n "$FAKE_AGENT_STARTED_FILE" ]; then
	printf '%s' 'started' > "$FAKE_AGENT_STARTED_FILE"
fi
if [ -n "$FAKE_AGENT_PRINT_ENV" ]; then
	printenv "$FAKE_AGENT_PRINT_ENV"
fi
case "$1" in
  "sleep "*) exec sleep "${1#sleep }" ;;
  *) printf '%s\n' "$@" ;;
esac
`
		bin = filepath.Join(dir, name)
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}

	path := os.Getenv("PATH")
	if path != "" {
		path = string(os.PathListSeparator) + path
	}
	t.Setenv("PATH", dir+path)
}
