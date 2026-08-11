#!/usr/bin/env node

import {
  copyFileSync,
  lstatSync,
  mkdirSync,
  renameSync,
  rmSync,
} from "node:fs";
import { homedir } from "node:os";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repoRoot = fileURLToPath(new URL("..", import.meta.url));
const sourceRoot = join(repoRoot, "skills", "cs-cloud-task");
const costrictHome = resolve(process.env.COSTRICT_HOME || join(homedir(), ".costrict"));
const skillsRoot = join(costrictHome, "skills");
const targetRoot = join(skillsRoot, "cs-cloud-task");
const stagingRoot = join(skillsRoot, `.cs-cloud-task-${process.pid}.staging`);
const backupRoot = join(skillsRoot, `.cs-cloud-task-${process.pid}.backup`);

const files = ["SKILL.md"];

function removeIfPresent(path) {
  rmSync(path, { force: true, recursive: true });
}

mkdirSync(skillsRoot, { recursive: true });
removeIfPresent(stagingRoot);
removeIfPresent(backupRoot);

try {
  try {
    if (lstatSync(targetRoot).isSymbolicLink()) {
      throw new Error(`refusing to replace symlinked skill directory: ${targetRoot}`);
    }
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }

  for (const relativePath of files) {
    const destination = join(stagingRoot, relativePath);
    mkdirSync(dirname(destination), { recursive: true });
    copyFileSync(join(sourceRoot, relativePath), destination);
  }
  let hadExistingTarget = false;
  try {
    lstatSync(targetRoot);
    hadExistingTarget = true;
    renameSync(targetRoot, backupRoot);
  } catch (error) {
    if (error.code !== "ENOENT") throw error;
  }

  try {
    renameSync(stagingRoot, targetRoot);
  } catch (error) {
    if (hadExistingTarget) renameSync(backupRoot, targetRoot);
    throw error;
  }
  removeIfPresent(backupRoot);
  process.stdout.write(`Installed cs-cloud-task to ${targetRoot}\n`);
} finally {
  removeIfPresent(stagingRoot);
}
