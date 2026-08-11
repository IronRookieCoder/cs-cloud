import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import {
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  symlinkSync,
  writeFileSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));
const installer = join(repoRoot, "scripts", "install-cs-cloud-task-skill.mjs");

test("installs only runtime skill files into the CoStrict skill directory", () => {
  const costrictHome = mkdtempSync(join(tmpdir(), "costrict-home-"));

  execFileSync(process.execPath, [installer], {
    env: { ...process.env, COSTRICT_HOME: costrictHome },
    stdio: "pipe",
  });

  const installed = join(costrictHome, "skills", "cs-cloud-task");
  assert.match(readFileSync(join(installed, "SKILL.md"), "utf8"), /name: cs-cloud-task/);
  assert.equal(existsSync(join(installed, "agents")), false);
  assert.equal(existsSync(join(installed, "scripts")), false);

  writeFileSync(join(installed, "stale-file.txt"), "stale");
  execFileSync(process.execPath, [installer], {
    env: { ...process.env, COSTRICT_HOME: costrictHome },
    stdio: "pipe",
  });
  assert.equal(existsSync(join(installed, "stale-file.txt")), false);
});

test("refuses to replace a symlinked skill destination", () => {
  const costrictHome = mkdtempSync(join(tmpdir(), "costrict-home-"));
  const decoy = mkdtempSync(join(tmpdir(), "cs-cloud-task-decoy-"));
  const skillsRoot = join(costrictHome, "skills");
  const installed = join(skillsRoot, "cs-cloud-task");
  mkdirSync(skillsRoot, { recursive: true });
  writeFileSync(join(decoy, "marker.txt"), "keep");
  symlinkSync(decoy, installed, process.platform === "win32" ? "junction" : "dir");

  assert.throws(() => {
    execFileSync(process.execPath, [installer], {
      env: { ...process.env, COSTRICT_HOME: costrictHome },
      stdio: "pipe",
    });
  });
  assert.equal(readFileSync(join(decoy, "marker.txt"), "utf8"), "keep");
});
