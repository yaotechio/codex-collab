# Releasing

The single source of truth for a release is the **git tag `vX.Y.Z`**. Pushing it
triggers `.github/workflows/release.yml`, which builds the prebuilt binaries,
publishes the npm packages, and creates the GitHub Release.

## One-time setup

1. **npm**: an npm account that owns the `@yaotechio` org (scoped packages).
2. **GitHub secret**: add an npm **automation** token as the repo secret
   `NPM_TOKEN` (Settings → Secrets and variables → Actions). CI uses it to publish.
3. The repo must be **public** (so users can `npx`/install without auth).

## Version must be consistent in 4 places

Before tagging, set the same `X.Y.Z` in all of these (CI hard-fails on mismatch):

| File | Field |
|---|---|
| `.claude-plugin/plugin.json` | `"version"` |
| `.mcp.json` | `npx … @yaotechio/codex-collab@X.Y.Z` |
| `hooks/hooks.json` | `npx … @yaotechio/codex-collab@X.Y.Z hook` |
| git tag | `vX.Y.Z` |

(The npm package versions and the Go `main.version` string are stamped
automatically at build time from the tag — no manual edit needed. The server
version comes from `-ldflags "-X main.version=…"`.)

## Cut a release

```bash
# 1. bump the three files above to the new X.Y.Z, commit
git commit -am "release vX.Y.Z"

# 2. tag and push — CI does the rest
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin main vX.Y.Z
```

CI then:
1. verifies the version is consistent across the three files;
2. runs `go test`;
3. `node scripts/build-npm.mjs --version X.Y.Z --publish` — cross-compiles all
   platforms and publishes the main package + the 6 per-platform packages to npm
   (`--access public`);
4. `make dist` and attaches the raw binaries to a generated GitHub Release.

Users pick up the new version via `/plugin update codex-collab@yaotechio`
(or Claude Code's startup auto-update).

## Publish from your machine instead (no CI)

```bash
npm login                                  # once
make npm VERSION=X.Y.Z PUBLISH=1           # build all platforms + npm publish
```

`make npm VERSION=X.Y.Z` (without `PUBLISH=1`) just stages everything under
`dist/npm/` so you can inspect it before publishing.

## Versioning policy

- Pre-1.0: bump MINOR (`0.x.0`) for features / distribution changes, PATCH
  (`0.x.y`) for fixes.
- Never reuse or move a published tag/version — npm and the Go module proxy
  treat versions as immutable. Always go forward.
