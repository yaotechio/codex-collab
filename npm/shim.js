#!/usr/bin/env node
"use strict";
// Launcher for the codex-collab MCP server. This npm package is a thin wrapper:
// the real program is a prebuilt Go binary shipped in a per-platform optional
// dependency (@yaotech/codex-collab-<platform>-<arch>). npm installs only the
// one matching the host (via each subpackage's "os"/"cpu" fields); this shim
// resolves that binary and execs it, forwarding argv so `... hook` / `... fmt`
// subcommands pass straight through. No Go toolchain is required at runtime.
const { spawnSync } = require("node:child_process");

const platform = process.platform; // darwin | linux | win32
const arch = process.arch; // arm64 | x64 | ...
const pkg = `@yaotech/codex-collab-${platform}-${arch}`;
const binName = "codex-collab" + (platform === "win32" ? ".exe" : "");

let binPath;
try {
  binPath = require.resolve(`${pkg}/bin/${binName}`);
} catch {
  console.error(
    `[codex-collab] no prebuilt binary for ${platform}-${arch} ` +
      `(missing optional dependency ${pkg}).\n` +
      `Build from source instead: https://github.com/yaotechio/codex-collab`
  );
  process.exit(1);
}

const res = spawnSync(binPath, process.argv.slice(2), { stdio: "inherit" });
if (res.error) {
  console.error(`[codex-collab] failed to start binary: ${res.error.message}`);
  process.exit(1);
}
process.exit(res.status === null ? 1 : res.status);
