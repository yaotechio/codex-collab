#!/usr/bin/env node
// Build the npm distribution for codex-collab into ./dist/npm:
//   - one per-platform package (@yaotech/codex-collab-<os>-<arch>) holding the
//     cross-compiled Go binary, with "os"/"cpu" so npm installs only the match;
//   - the main wrapper package (@yaotech/codex-collab) carrying shim.js and
//     listing the per-platform packages as optionalDependencies.
//
// Usage:
//   node scripts/build-npm.mjs --version 0.2.0            # build only
//   node scripts/build-npm.mjs --version 0.2.0 --publish  # build + npm publish
//
// Publishing needs npm auth (npm login locally, or NODE_AUTH_TOKEN in CI).
// Scoped packages are published with --access public.
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, rmSync, cpSync, writeFileSync, readFileSync, chmodSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));

// --- args ---
const args = process.argv.slice(2);
const version = valueOf("--version");
const publish = args.includes("--publish");
if (!version || !/^\d+\.\d+\.\d+/.test(version)) {
  fail("missing or invalid --version (expected x.y.z)");
}

function valueOf(flag) {
  const i = args.indexOf(flag);
  return i >= 0 && i + 1 < args.length ? args[i + 1] : undefined;
}
function fail(msg) {
  console.error(`build-npm: ${msg}`);
  process.exit(1);
}
function run(cmd, cmdArgs, opts = {}) {
  execFileSync(cmd, cmdArgs, { stdio: "inherit", ...opts });
}

// npm platform/arch -> Go GOOS/GOARCH
const TARGETS = [
  { os: "darwin", cpu: "arm64", goos: "darwin", goarch: "arm64" },
  { os: "darwin", cpu: "x64", goos: "darwin", goarch: "amd64" },
  { os: "linux", cpu: "x64", goos: "linux", goarch: "amd64" },
  { os: "linux", cpu: "arm64", goos: "linux", goarch: "arm64" },
  { os: "win32", cpu: "x64", goos: "windows", goarch: "amd64" },
  { os: "win32", cpu: "arm64", goos: "windows", goarch: "arm64" },
];

const outDir = join(root, "dist", "npm");
rmSync(outDir, { recursive: true, force: true });
mkdirSync(outDir, { recursive: true });

const ldflags = `-s -w -X main.version=${version}`;
const platformPkgDirs = [];

console.log(`build-npm: building ${TARGETS.length} platform packages @ ${version}`);
for (const t of TARGETS) {
  const pkgName = `@yaotech/codex-collab-${t.os}-${t.cpu}`;
  const pkgDir = join(outDir, `codex-collab-${t.os}-${t.cpu}`);
  const binName = "codex-collab" + (t.os === "win32" ? ".exe" : "");
  const binPath = join(pkgDir, "bin", binName);
  mkdirSync(join(pkgDir, "bin"), { recursive: true });

  run("go", ["build", "-ldflags", ldflags, "-o", binPath, "."], {
    cwd: root,
    env: { ...process.env, GOOS: t.goos, GOARCH: t.goarch, CGO_ENABLED: "0" },
  });
  if (t.os !== "win32") chmodSync(binPath, 0o755);

  writeFileSync(
    join(pkgDir, "package.json"),
    JSON.stringify(
      {
        name: pkgName,
        version,
        description: `codex-collab prebuilt binary for ${t.os}-${t.cpu}`,
        os: [t.os],
        cpu: [t.cpu],
        files: ["bin/"],
        license: "MIT",
        repository: { type: "git", url: "git+https://github.com/yaotechio/codex-collab.git" },
        homepage: "https://github.com/yaotechio/codex-collab",
      },
      null,
      2
    ) + "\n"
  );
  platformPkgDirs.push(pkgDir);
  console.log(`  ✓ ${pkgName}`);
}

// --- main wrapper package ---
const mainDir = join(outDir, "codex-collab");
mkdirSync(mainDir, { recursive: true });
cpSync(join(root, "npm", "shim.js"), join(mainDir, "shim.js"));
const mainPkg = JSON.parse(readFileSync(join(root, "npm", "package.json"), "utf8"));
mainPkg.version = version;
for (const t of TARGETS) {
  mainPkg.optionalDependencies[`@yaotech/codex-collab-${t.os}-${t.cpu}`] = version;
}
writeFileSync(join(mainDir, "package.json"), JSON.stringify(mainPkg, null, 2) + "\n");
console.log(`  ✓ @yaotech/codex-collab (main)`);

if (!publish) {
  console.log(`\nbuild-npm: staged in ${outDir} (not published; pass --publish to publish)`);
  process.exit(0);
}

// Publish platform packages first so the main package's optionalDependencies resolve.
// Run npm from the repo root (where CI's auth .npmrc lives) and pass the package
// directory as an argument, so the auth token is found regardless of subdir cwd.
console.log(`\nbuild-npm: publishing to npm…`);
for (const dir of platformPkgDirs) {
  run("npm", ["publish", dir, "--access", "public"], { cwd: root });
}
run("npm", ["publish", mainDir, "--access", "public"], { cwd: root });
console.log(`build-npm: published @ ${version}`);
