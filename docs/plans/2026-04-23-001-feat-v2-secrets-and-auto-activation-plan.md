---
title: "feat: v2.0 Secrets separation and auto-activation allow-list"
type: feat
status: active
date: 2026-04-23
deepened: 2026-04-23
---

# feat: v2.0 Secrets separation and auto-activation allow-list

## Overview

Ship the two coupled v2.0 features tracked in [#4](https://github.com/dalley/ccp/issues/4) and [#5](https://github.com/dalley/ccp/issues/5):

- **#5 — Secrets separation + `{{ ... }}` reference resolution.** New `ccp secret {set,get,list,rm}` CLI backed by the OS keychain (`zalando/go-keyring`) with a file fallback at `~/.config/ccp/secrets/<profile>.json`. Profile files may embed `{{ keychain://... }}`, `{{ op://... }}`, and `{{ env.FOO }}` refs; at activation time, any file containing refs is **rendered** to the runtime dir (0600) rather than symlinked. New `profile audit` flags high-entropy strings; new `profile export` strips secrets unless `--include-secrets`.
- **#4 — Auto-activation via `.claude-profile` marker with direnv-style allow-list.** New `ccp allow` / `ccp deny` commands hash-pin a project's `.claude-profile` under `~/.config/ccp/allowlist.toml`. A new layer in the shell-init snippet walks up from `$PWD`, consults the allow-list via a hidden `ccp shell-resolve-dir` helper, and exports `CLAUDE_CONFIG_DIR` when a safe match is found. Fail-closed: unrecognized or drifted markers warn once per shell session and do nothing.

Both issues cross-reference each other: the authors' original framing was that auto-activation is unsafe without secrets separation. On closer review, the coupling is weaker than stated — the allow-list is the actual supply-chain defense; secrets separation is an orthogonal quality-of-life improvement. They are bundled here for release packaging (shared foundation work, single v2.0 release) rather than safety. **After Unit 1 lands, the two arcs can proceed in parallel** — Units 2–7 (secrets) and Units 8–10 (allow-list) have no interdependency.

## Problem Frame

Today, two pain points bound ccp's usefulness:

1. **Manual switching costs compound**: users on multiple clients must `ccp use foo` or `export CCP_PROFILE=foo` every time they change context. direnv solved this pattern for env vars a decade ago; we should adopt its security model, not invent one.
2. **Anything in `settings.json` is git-synced in cleartext.** A user who inlines `env.ANTHROPIC_API_KEY` or an MCP server with an embedded credential will push it to GitHub. Today's `settings.json` → sync repo pipeline has no cryptographic off-ramp, and existing `~/.config/ccp/secrets/<name>.json` is only "reserved" — no code creates it, reads it, or resolves refs against it.

The two features couple because auto-activation — silent, on-`cd`, without explicit confirmation — is a supply-chain vulnerability the moment a malicious commit in a cloned repo can drop a `.claude-profile` that resolves to a profile containing hostile `hooks/`. direnv's fail-closed allow-list is the answer. But that answer only fully works if profiles don't leak secrets in the first place — otherwise the "safe to auto-activate" property holds only for profiles a user has manually audited for every inlined API key.

## Requirements Trace

Mapped from the acceptance criteria in each issue:

**From #5 (secrets):**
- R1. `ccp secret set work ANTHROPIC_API_KEY sk-…` stores in the OS keychain; `security find-generic-password -a work -s ccp` retrieves it on macOS.
- R2. A `settings.json` containing `"apiKey": "{{ keychain://ccp/work/ANTHROPIC_API_KEY }}"` resolves at activation time into a real 0600 file under `~/.claude-work/`.
- R3. `ccp profile export` never emits resolved secrets without `--include-secrets` and a confirmation.
- R4. `ccp profile audit` detects a planted AWS access-key pattern.
- R5. On a machine missing the referenced keychain entry, `ccp use` surfaces a clear error instead of writing a file with the literal `{{ ... }}` string.
- R6. Sync push never touches `secrets/` or keychain entries (already gitignored — verify still holds).

**From #4 (auto-activation):**
- R7. In a project with an allowed `.claude-profile`, `cd project/` activates the profile without user interaction.
- R8. Editing `.claude-profile` after allow → activation refused with a clear warning; `ccp allow` re-approves.
- R9. An unauthorized `.claude-profile` committed to a repo and cloned fresh never activates.
- R10. `cd` in a directory without a marker does not stomp a manually-set `CLAUDE_CONFIG_DIR`.
- R11. Hook benchmark: <50ms warm cache; <100ms cold.
- R12. `CCP_PROFILE_AUTO=0` disables the entire auto layer.

## Scope Boundaries

- **Windows support is out of scope (v2.1).** On Windows, new commands (`ccp secret`, `ccp allow`, `ccp deny`, `ccp profile audit`, `ccp profile export`) are **not registered at all** — `ccp --help` on Windows does not list them, and invoking them yields cobra's `unknown command` error (mapped to `ExitUser`). This is cleaner than a runtime `ErrUnsupportedPlatform` which misleads agents into retrying as if it were transient state. New packages carry `//go:build !windows` guards matching `internal/fslock`'s existing convention. PowerShell shell-init continues to return "unsupported shell".
- **Devcontainers / Docker**: treated as a subset of "headless Linux" — keychain falls back to the file store with a loud warning. Auto-activation is usable inside the container but allow-list entries do not transit the container boundary (host allow ≠ container allow). Documented explicitly; not papered over.
- **Shared secrets across profiles ("scope: shared")** is deferred per #5's own note. Secrets are per-profile only in v2.0.
- **`ccp profile import`** (counterpart to `export`) is deferred. v2.0 ships export only, so users can hand off the tarball; import is a v2.1 follow-up.
- **Encrypted file fallback** is out of scope — if the keychain is unavailable, we fall back to a 0600 file with a loud warning and do not invent our own crypto (per #5's explicit direction).
- **New template schemes beyond the three specified** are explicitly out of scope. The issue says "Keep the reference language minimal … resist adding more."
- **Hook perf benchmarking** is part of R11 but does not need to be a separate Go benchmark binary; a shell timing loop in the test is sufficient evidence. Budget is stated as "cache hit <5ms; cache miss (Go binary cold start dominates) 30–80ms typical, up to 150ms on a cold cache" — more realistic than the issue's <50ms/<100ms ceiling.

### Deferred to Separate Tasks

- **`ccp profile import`**: will land after v2.0 as a follow-up issue.
- **Windows Credential Manager backend**: tracked by #6 (Windows support).
- **Team profile import via Git URL**: tracked by #7.
- **Service-account / headless keyring strategies**: documented as a known limitation; an explicit runbook entry can follow separately.

## Context & Research

### Relevant Code and Patterns

- **Resolution order and shell snippet** live in `internal/cli/shellinit.go` and `internal/cli/shellactive.go`. The hidden `shell-active` command is the template for any new hot-path helper: always exit 0, never create directories, silent on error.
- **`BuildSymlinks`** is `internal/profile/symlinks.go:19-50`. The per-item loop is the plug point for render-vs-symlink. `ensureSymlink` already refuses to overwrite a non-symlink — the new code path needs its own "refuse to overwrite a non-ccp file" invariant.
- **Atomic-write + flock pattern** lives in `internal/manifest/manifest.go:62-91` (manifest.Save) and `internal/cli/state.go` (`withLockedState`). Copy exactly: `os.CreateTemp` → encode → `Sync` → `Rename`, all under `fslock.Acquire(p.LockPath)`.
- **CLI command file convention**: one file per leaf subcommand (`profile_create.go`, `sync_pull.go`, …). New files will follow the same split.
- **JSON-with-nonzero-exit pattern**: `profile_diff.go` and `profile_doctor.go` emit well-formed JSON to stdout, then return a sentinel so exit is nonzero. `profile audit` will do the same.
- **Live-sh execution test**: `internal/cli/shellinit_test.go:TestShellInitPosixActuallyRunsInSh` is the template for proving the new auto-activation snippet actually works end-to-end.
- **Hermetic test setup**: `internal/cli/m3_test.go:setupCLI / runCLI` handles `CCP_ROOT`, `XDG_CONFIG_HOME`, cobra invocation, and captured I/O. Canonical for every new CLI test.
- **Exit-code classification**: `internal/cli/exit.go:ExitCodeFor` — every new sentinel registers here and gets a row in `internal/cli/exit_test.go`.
- **Name validation**: `profile.ValidateName` is the regex gate everything else reuses; secrets use a similar constraint on key names.
- **Sync gitignore**: `internal/sync/sync.go:GitignoreContents` already reserves `/secrets/`; the new `/allowlist.toml` line goes into the same constant.

### Institutional Learnings

From this repo's own prior `ce-review` runs (`/.context/compound-engineering/ce-review/…`):

- **ADV-008 — `%q` is Go-escape, not POSIX-escape.** Any `export VAR=...` snippet must hand-roll POSIX single-quote escaping. Inline four-line helper; no new dependency.
- **SEC-SYMLINK-TOCTOU-WALK** — hashing a file for the allow-list must open with `O_NOFOLLOW`, stat-by-fd, hash-by-fd. Never re-resolve the path between check and use.
- **SEC-PROFILE-DIRS-WORLD-READABLE** — all new dirs holding secret-bearing content are 0700; files are 0600. Match existing repo standard.
- **REL-001 — EXDEV** — atomic renames across filesystems (e.g., `$HOME` on NFS) fail with EXDEV. Any new atomic-write path needs a `copyDir + RemoveAll` fallback detection.
- **fslock is per-FD, not per-process** — a long-running ccp (shell hook server mode, TUI) would need an in-process `sync.Mutex` alongside flock. Out of scope today but document.

External searches (`docs/solutions/` across plugins) returned **zero direct matches** on direnv-style allow-lists, keychain Go integrations, or `op` CLI patterns. Treat each as greenfield; rely on the research notes below and write new learnings as we go.

### External References

- **direnv security model** ([`direnv.1`](https://direnv.net/man/direnv.1.html)): allow file path is `$XDG_DATA_HOME/direnv/allow/<hash>`, hash is SHA-256 of `absolute_path + "\n" + contents`. Including the path defeats "copy a malicious marker into a renamed folder" attacks. We adopt the hashing scheme exactly but use one TOML file (`allowlist.toml`) instead of a directory of hashes — matches existing ccp conventions (manifest.toml), and we already have flock + atomic-write primitives.
- **`zalando/go-keyring` API and mocking**: use `keyring.MockInit()` in tests — do **not** wrap behind a custom interface. 1024-byte value limit on macOS/Windows; no limit on Linux Secret Service. Headless Linux (no D-Bus) returns an error at runtime — our fallback path catches that and writes to the file store with a warning.
- **1Password CLI (`op read`)**: shell out with a short context timeout. In non-TTY contexts (shell hook), refuse to call `op` when `OP_SERVICE_ACCOUNT_TOKEN` is unset, because `op` may prompt for biometric unlock and hang the user's shell. In TTY contexts (`ccp use`), call freely. Exit codes not publicly documented — treat any nonzero as failure and surface stderr.
- **POSIX single-quote escaping**: `strings.ReplaceAll(s, "'", "'\\''")` wrapped in outer single quotes. Avoid new dep (`alessio/shellescape`); the four lines are fine in-tree.
- **Shannon entropy** for the audit detector: threshold 3.5 for substrings ≥20 chars, combined with a small regex list (AWS `AKIA…`, GitHub `ghp_`/`gho_`/`ghu_`/`ghs_`/`ghr_`, Stripe `sk_live_/sk_test_`, Slack `xox[abprs]-…`, Google API `AIzaSy…`, JWT `eyJ…`, PEM blocks). Hand-roll; do not pull in gitleaks.

## Key Technical Decisions

1. **Phase order: ship #5 (secrets) before #4 (auto-activation).** The coupling is not "auto-activation is unsafe without secrets separation" (the allow-list is the supply-chain defense; #5 does not materially strengthen it). The real reason to bundle is release packaging and the shared foundation — `BuildSymlinks` render path, exit-code sentinels, CLI conventions. **After Unit 1 lands, Units 2–7 (secrets arc) and Units 8–10 (allow-list arc) are independent and may run in parallel**; Unit 11 joins at shell-init and Unit 12 is the final cleanup.

2. **Reference syntax is profile-implicit and delimiter-collision-safe:**
   - `{{ keychain:KEY }}` — resolved against the current profile's keychain entries (service=`ccp`, account=profile-name).
   - `{{ op://<vault>/<item>/<field> }}` — 1Password.
   - `{{ env.VAR }}` — process env.
   No service/account prefix on `keychain:` — refs are automatically correct when a profile is renamed or copied (addresses a named gap in review). Delimiters are `{{ ... }}` with surrounding whitespace tolerated. **Collision handling is strict**: `refs.HasRefs` matches only the three scheme signatures (`{{\s*(keychain:|op://|env\.)`), NOT any `{{`. A user's prose containing `{{ placeholder }}` or Helm's `{{ .Values.x }}` is not a ref, does not trigger rendering, and passes through verbatim. Malformed refs inside a recognized scheme are a hard error (tells the user their syntax is wrong). Unknown schemes do not exist — anything not matching HasRefs is not parsed.

3. **Escape sequence for literal refs**: a ref line immediately preceded by `{{!}}` is ignored — use when a user genuinely wants the literal string `{{ keychain:KEY }}` in their profile (e.g., in `CLAUDE.md` explaining the feature). Minimal, discoverable, documented.

4. **Keychain client is `zalando/go-keyring` used directly.** `keyring.MockInit()` covers tests. Tests in `internal/secret` must NOT use `t.Parallel()` (MockInit is package-level state); comment documents this as an invariant.

5. **Keychain error discrimination**: the plan distinguishes three keychain states via typed sentinels:
   - `ErrSecretNotFound` (keyring says key missing) → `ExitUser`
   - `ErrKeychainLocked` (keyring reachable but access denied; macOS locked, Linux Secret Service locked) → `ExitState` with clear message "unlock your keychain and retry"
   - `ErrKeychainUnavailable` (keyring backend unreachable; headless Linux, no D-Bus) → triggers file-store fallback with a loud one-time warning
   This prevents the "secret not found" misdiagnosis when the real problem is a locked keychain.

6. **File fallback** at `~/.config/ccp/secrets/<profile>.json` — 0600 perms, atomic write, flock. One JSON object `{KEY: VALUE}` per profile. Already gitignored. Warning emitted to stderr once per process via `sync.Once` (CLI is one-shot, so effectively once per invocation).

7. **Render-vs-symlink boundary**: in `BuildSymlinks`, for each top-level `SharedItems` entry:
   - **File items** (e.g., `settings.json`): open with `O_NOFOLLOW`, run `refs.HasRefs(bytes)`. If true → render via `refs.Render` into `pr.ConfigDir/<name>` as a regular file preserving source mode bits masked against 0755 (so an executable stays executable; 0644 stays 0644) and bounded below by 0600 (no world-read for rendered content). If false → existing symlink path.
   - **Directory items** (e.g., `hooks/`, `skills/`): walk the source dir once to check if ANY file under it has refs. If none: symlink the directory as today. If any: materialize `pr.ConfigDir/<name>` as a 0700 directory, then recurse — for each source entry, symlink when no refs, render when refs present. Subdirectories under a ref-bearing top-level are recreated as real 0700 dirs.
   - **Directory mode transitions** (symlink → real dir when a ref is added; real dir → symlink when all refs are removed) are explicit operations: detect current runtime state, `os.Remove` the symlink or walk-and-delete the real dir (only removing entries owned by ccp — symlinks and rendered files with the atomic-rename signature), then rebuild in the new form.

8. **Render atomicity and partial-failure policy**: each rendered file writes via temp-file-in-same-dir + rename (mode set on the temp file before rename). On render failure for file X, `BuildSymlinks` continues processing remaining files (best-effort progress), collects errors, and returns a joined error at the end. The runtime dir is left in the most-complete state achievable — successfully rendered files persist, failures leave the prior rendered content intact. Partial failure is surfaced clearly (exit `ExitState`, message lists unresolved refs).

9. **`Doctor` is re-taught**: pass 1 peeks at the source file; if it matches `refs.HasRefs`, a runtime regular file is expected, not a warning. Pass 2 also flags **unresolved refs in source** as a warn (missing keychain entry for a ref this profile declares) — a user-facing health check that surfaces the "you moved to a new machine and forgot to `ccp secret set`" case without requiring them to run `ccp use` and fail.

10. **`ccp exec` refresh is opt-in-by-default-scoped**: `exec` calls `RefreshSymlinks` ONLY when `refs.HasAnyRefs(pr.SourceDir)` returns true (a cheap scan — byte-level substring check on each source file with early exit on first match). Profiles without refs pay zero extra cost; profiles with refs pay the refresh cost once per invocation. A `--no-refresh` flag from day one overrides for power users who want the legacy env-var-only behavior. The refresh itself runs under `withLock` to serialize with other ccp state mutations.

11. **Allow-list storage is a single TOML file** at `~/.config/ccp/allowlist.toml` with schema version 1 and an `[entries]` map. Each entry is keyed by the **absolute path of the marker** for lookup convenience, but the **stored hash is of content only** — `SHA-256(<file-contents>)`, computed from a `O_NOFOLLOW`-opened fd.

12. **Content-only hash deliberately diverges from direnv's `path+content` scheme** because ccp's headline feature is "sync profiles across machines" and direnv's model breaks that (README's own tagline: "sync them across machines via Git"). Content-only hashing is safe for ccp's threat model: the marker contents are just a profile name, validated against `^[a-z][a-z0-9_-]{0,62}$`, and the actual execution surface is the profile's `hooks/` which the user has ALREADY vetted when creating the profile locally. A malicious renamed folder must match both the content AND have an already-created-local profile of the correct name — the attack does not clear the bar. Document this tradeoff explicitly in README's Gotchas.

13. **Allow-list concurrency**: `ccp allow` / `ccp deny` acquire the global flock (`p.LockPath`) and do load → mutate → atomic-save inside the lock. `ccp shell-resolve-dir` (hot path) reads the file **without** taking the lock — it relies on the atomic-rename save for consistency (reader sees the whole old state or the whole new state, never torn). This is codified as an invariant in Unit 10's doc comment.

14. **Shell hook gets a cache tier.** The hook is a shell function that checks `$CLAUDE_CONFIG_DIR` (escape hatch), `$CCP_PROFILE` (per-shell override), `$CCP_PROFILE_AUTO` (disable), then the cache (last-seen marker path + mtime for a hot-path equality check), and only then forks `ccp shell-resolve-dir`. Negative cache: if no marker was found at `$PWD`, cache the deepest path walked; subsequent `cd`s are a cache hit **only when `$PWD` is an ancestor of the cached no-marker dir, or equal to it**. Sibling dirs invalidate the negative cache (they are valid targets for a new marker).

15. **`ccp shell-resolve-dir <dir>` is a new hidden command.** Walks up from `<dir>`, finds a `.claude-profile`, re-opens with `O_NOFOLLOW`, hashes contents, checks the allow-list, and prints (on a match) three `KEY=value` lines separated by newlines. Silent, fast, always exits 0. **Output format**: the hook consumes output via `eval` with every value wrapped in `shellQuote` — profile names are regex-validated, marker paths can contain arbitrary chars but `shellQuote` handles them. `CCP_AUTO_WARN` values are hardcoded constants (`drift`, `unallowed`) — never user-controlled. **Symlinked markers are silently skipped** (O_NOFOLLOW yields ELOOP → fail closed, no warning — documented as invariant to prevent future maintainers adding a warning that creates an existence oracle).

16. **Drift UX is louder than "warn once per shell":**
   - First drift encounter in a shell session: stderr one-liner warning with the marker path and a `ccp allow --status` hint.
   - Subsequent drift for the same marker in the same shell: silent (single-path guard via `CCP_AUTO_WARNED_<hash>`).
   - Every new shell session: fresh warning (so opening a new tab re-surfaces the issue).
   - `ccp exec` and `ccp use` detect when the active profile disagrees with the auto-resolved profile and emit a short advisory — catches users whose `cd` silently failed.

17. **1Password resolver is TTY + context-aware**: `refs.Render` takes a `context.Context` the caller supplies. Callers use appropriate timeouts:
   - Shell hook (`shell-resolve-dir`): never resolves refs at all — it only hashes the marker and checks the allow-list. The `op` path is cold.
   - `ccp use` / `ccp exec` with TTY: 30-second timeout — enough for biometric unlock including Touch ID prompt.
   - `ccp use` / `ccp exec` without TTY and without `OP_SERVICE_ACCOUNT_TOKEN`: refuse — emits `ErrSecretRefUnresolved` with message "`op` may prompt interactively; set OP_SERVICE_ACCOUNT_TOKEN for non-interactive use".
   - `ccp use` / `ccp exec` without TTY with `OP_SERVICE_ACCOUNT_TOKEN`: 5-second timeout — service accounts are fast; this catches wedged networks without blocking long.

18. **Profile export defaults are conservative and redaction-first:**
   - `ccp profile export <name>` emits a tar stream. Default writes to stdout; refuses if stdout is a TTY and `-o` is unset.
   - **Default** strips secrets: files with refs are NOT resolved — they are emitted verbatim (the `{{ ref }}` syntax transports cleanly); the per-profile `secrets/<name>.json` file is omitted entirely. This is the reversible form — the recipient resolves refs against their own keychain.
   - `--include-secrets` resolves refs and inlines values (cleartext). Requires TTY confirmation via `confirm()`. In non-TTY contexts, requires `--yes-really` as an explicit opt-in. Emits an `EXPORT_MANIFEST.json` into the tar with `contains_secrets: true`, a list of inlined files, and a timestamp — so future `ccp profile import` can re-prompt.
   - **Audit gate is opt-in, not default**: the plan previously defaulted to refusing export on any audit finding; this causes Shannon-entropy false-positives (UUIDs, git hashes, base64 blobs) to block legitimate exports. Revised: `--fail-on-audit` enables the gate explicitly. Without the flag, audit findings are advisory (printed to stderr as a hint, export proceeds). `--skip-audit` skips entirely.

19. **Audit detector: regex prefilter + entropy confirmation, not either/or:**
   - Prefix-regex pass: AWS `AKIA/ASIA/…`, GitHub `ghp_/gho_/ghu_/ghs_/ghr_/github_pat_`, Stripe `sk_(test|live|prod)_`, Slack `xox[abprs]-`, Google `AIzaSy`, JWT `eyJ…` (three base64 segments), PEM `-----BEGIN … PRIVATE KEY-----`. These are high-precision: emit findings directly.
   - Shannon-entropy pass: only triggers when a substring also passes a base64/hex charset filter AND is ≥30 chars (raised from 20) AND entropy ≥ 4.0 (raised from 3.5, matches gitleaks' base64-segment default more closely). This reduces UUID/hash false positives substantially.
   - Ref-bearing values (`{{ keychain:... }}`) are not scanned.

20. **POSIX escaping helper inline.** `func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }` lives next to the shell-init snippet emitter. Used anywhere a user-controlled value crosses into a shell snippet. Test cases include: path with spaces, path with single quote, path with dollar-sign, path with newline, path with backtick, empty string.

21. **`.claude-profile` marker format is strict**: exactly one line matching `^[a-z][a-z0-9_-]{0,62}$` optionally followed by a single trailing `\n`. Leading UTF-8 BOM, CRLF, zero-width whitespace, and trailing whitespace are all rejected with a clear error pointing the user at the byte offset of the first invalid char. Content-bytes are hashed as-is (bytes with the BOM fail validation and never reach the hash). Documented in README.

22. **First-run migration aid**: on the first `ccp use` after upgrading to v2.0, if `manifest.SchemaVersion` was previously 1 and is now 1 (no schema change — detected via a new `manifest.FirstRunAfterUpgrade` boolean that triggers once), emit a one-time stderr message recommending `ccp profile audit` across all profiles. Additionally, `ccp sync push` runs `audit` in advisory mode on the active profile and prints an stderr warning ("N suspected secrets detected in this profile; review with `ccp profile audit`") — never blocks, just informs. This catches users who had inlined secrets pre-v2 and gives them a clear path to remediation.

23. **Renaming `RefreshSymlinks`'s scope**: the function keeps its name but grows new side effects (resolving secrets, rendering files, shelling out to `op`). The doc comment makes this explicit. If implementation time reveals the semantic drift is confusing, we can introduce a `MaterializeRuntime` wrapper and deprecate `RefreshSymlinks` — deferred.

## Open Questions

### Resolved During Planning

- **Phase order?** → #5 and #4 are bundled for release packaging, not safety coupling (the allow-list is the supply-chain defense; secrets separation is an orthogonal feature). After Unit 1, Units 2–7 and Units 8–10 can proceed in parallel.
- **Allow-list format?** → Single `allowlist.toml`, schema-versioned `[entries]` map. Matches ccp's existing `manifest.toml` conventions.
- **Include path in the allow hash?** → **No** — content-only hash. Including path breaks ccp's "sync profiles across machines" tagline. Threat-model analysis in decision 12.
- **Reference syntax?** → Profile-implicit `{{ keychain:KEY }}` (no service/account prefix), plus `{{ op://… }}` and `{{ env.FOO }}`. Renaming or copying a profile doesn't break refs.
- **Delimiter collision?** → `HasRefs` matches scheme signatures only (`{{\s*(keychain:|op://|env\.)`), not bare `{{`. Prose and Helm templates pass through verbatim. Escape sequence `{{!}}` preceding a ref makes it literal.
- **How does `doctor` know a rendered file isn't a Claude-overwrite?** → Stateless: re-read the source at check time and run `refs.HasRefs`. No sidecar files, no manifest state.
- **Interface abstraction for keychain?** → No. Use `zalando/go-keyring` functions directly; `keyring.MockInit()` covers tests. No `t.Parallel()` in keychain tests.
- **Keychain locked vs not-found?** → Three typed sentinels: `ErrSecretNotFound` (ExitUser), `ErrKeychainLocked` (ExitState), `ErrKeychainUnavailable` (triggers file fallback).
- **Where should `ccp exec` reconcile rendered secrets?** → Scoped refresh: only when `refs.HasAnyRefs(pr.SourceDir)` returns true. `--no-refresh` flag for power users. Runs under `withLock`.
- **TTY vs non-TTY + timeout for `op` resolution?** → Resolver takes `context.Context`. TTY path uses 30s timeout (biometric unlock is slow). Non-TTY with service account token: 5s. Non-TTY without token: refuse with clear hint. Shell hook never resolves refs at all.
- **Export output target?** → stdout by default; `-o <path>` for file; refuse tar to TTY stdout.
- **Audit gate on export?** → Opt-in (`--fail-on-audit`), not default. Shannon-entropy false positives would block legitimate exports.
- **Audit detector granularity?** → Regex prefilter + entropy confirmation. Prefix regexes emit directly; entropy requires charset match + ≥30 chars + ≥4.0 entropy.
- **`.claude-profile` format?** → Single line matching `^[a-z][a-z0-9_-]{0,62}$` + optional `\n`. BOM/CRLF/whitespace rejected with a clear byte-offset error.
- **Migration for existing users?** → First-run advisory message on post-v2 `ccp use`; `ccp sync push` runs audit in advisory mode.
- **Windows command handling?** → Hide commands on Windows entirely (not runtime-error). Agents see `unknown command` not `ErrUnsupportedPlatform`.
- **Cache invalidation for negative marker cache?** → `$PWD` must be an ancestor of or equal to the cached no-marker dir; siblings invalidate.

### Deferred to Implementation

- **Exact error string for the keychain-fallback warning** — wording emerges during Unit 3.
- **Exact shell-hook cache variable names** (`CCP_AUTO_MARKER`, `CCP_AUTO_MARKER_MTIME`, `CCP_AUTO_NOMARKER_ROOT`, `CCP_AUTO_WARNED_*`) — may adjust for fish compatibility during Unit 11.
- **Whether to rename `RefreshSymlinks` to `MaterializeRuntime`** once it grows secret-resolution side effects — observe during Unit 4; decide based on whether maintainers find the existing name misleading.
- **Exact timing instrumentation inside `ccp shell-resolve-dir`** — the <25ms budget is a warm-process target; Go cold-start dominates on first invocation (30–80ms typical, up to 150ms with Gatekeeper translation on Apple Silicon). Timing test should be qualitative ("<200ms with populated tree") to avoid flakes.
- **Doctor warning shape for "ref declared but not resolvable"** — a new finding severity, but exact JSON field naming decided alongside Unit 4.
- **Whether `--include-secrets` tars should default to `.tar.age`-encouraged filenames** or carry a "contains secrets" header that `import` enforces — likely the latter, but shape finalizes during Unit 7.

## Output Structure

Partial file tree of new additions (existing files are modified, not shown):

    internal/
      allowlist/
        allowlist.go              # TOML load/save, hash, Approve, Revoke, Check
        allowlist_test.go
        allowlist_windows.go      # stub returning ErrUnsupportedPlatform
      refs/
        refs.go                   # parser + Resolver (keychain / op / env)
        refs_test.go
        refs_windows.go
      secret/
        secret.go                 # keychain + file fallback CRUD
        secret_test.go
        secret_windows.go
    internal/cli/
      allow.go                    # `ccp allow` / `ccp deny` / `ccp allow --status`
      allow_test.go
      profile_audit.go
      profile_audit_test.go
      profile_export.go
      profile_export_test.go
      secret.go                   # group command `ccp secret`
      secret_set.go
      secret_get.go
      secret_list.go
      secret_rm.go
      secret_test.go
      shell_resolve_dir.go        # hidden helper
      shell_resolve_dir_test.go
    docs/plans/
      2026-04-23-001-feat-v2-secrets-and-auto-activation-plan.md   # this file

## High-Level Technical Design

> *This illustrates the intended approach and is directional guidance for review, not implementation specification. The implementing agent should treat it as context, not code to reproduce.*

### Reference resolution path (Issue #5)

```text
            ~/.config/ccp/profiles/work/settings.json (source)
                       |
                       |  contains "{{ keychain://ccp/work/API_KEY }}"
                       v
            BuildSymlinks / RefreshSymlinks
                       |
        +--------------+---------------+
        | scan file bytes for "{{"     |
        +--------------+---------------+
                       v
        +-----------------------------+
        |   refs.Render(src, resolver)|
        +--------------+--------------+
                       |
         +-------------+-------------+
         |             |             |
         v             v             v
  keychain://  op://<ref>      env.FOO
   (go-keyring)  (exec op)     (os.LookupEnv)
         |             |             |
         +-------------+-------------+
                       |
                       v
               rendered bytes
                       |
                       v
   ~/.claude-work/settings.json (mode 0600, regular file)
```

### Auto-activation hook precedence (Issue #4)

```text
  chpwd / PROMPT_COMMAND / fish PWD event fires
                 |
                 v
        __ccp_activate (shell function)
                 |
  $CLAUDE_CONFIG_DIR set?  --yes--> return (escape hatch)
                 | no
                 v
  $CCP_PROFILE_AUTO = 0?   --yes--> skip auto layer; fall through to v1 logic
                 | no
                 v
  walk up for .claude-profile      (pure shell, ~0-5ms)
                 |
            found?
          /        \
         no         yes
          |          |
          |     mtime+path cache hit?  ---yes---> use cached CCP_PROFILE
          |          | no
          |          v
          |     exec `ccp shell-resolve-dir $PWD`
          |          |
          |     prints CCP_PROFILE=... / empty
          |          |
          |          v
          |     eval + update cache vars
          |          |
          |  if empty: warn-once on drift / no-allow
          |          |
          v          v
  $CCP_PROFILE set?   --yes--> CLAUDE_CONFIG_DIR=$HOME/.claude-$CCP_PROFILE
                 | no
                 v
            return (no activation)
```

### Allow-list state and hash

```text
  file at path P with contents C
           |
           v
  hash = SHA256( realpath(P) + "\n" + C )
           |
           v
  allowlist.toml:
  schema_version = 1
  [entries]
  "/Users/dalley/code/repo-a/.claude-profile" = "sha256:abcd…"
  "/Users/dalley/code/repo-b/.claude-profile" = "sha256:efgh…"
```

`Check(path)` opens with `O_NOFOLLOW`, hashes by fd, compares to the recorded entry. States: `Unallowed` (no entry), `HashMismatch` (entry exists, drift), `Allowed`.

## Implementation Units

- [ ] **Unit 1: Paths, sentinels, and exit-code wiring**

**Goal:** Add path helpers and typed errors that every subsequent unit depends on.

**Requirements:** Prerequisite for R1–R12.

**Dependencies:** None.

**Files:**
- Modify: `internal/paths/paths.go`
- Modify: `internal/paths/paths_test.go`
- Modify: `internal/cli/exit.go`
- Modify: `internal/cli/exit_windows.go`
- Modify: `internal/cli/exit_test.go`

**Approach:**
- Add to `Paths`: `SecretsDir` (`<ConfigDir>/secrets`), `AllowlistPath` (`<ConfigDir>/allowlist.toml`), and `SecretFilePath(name string) string` returning `<SecretsDir>/<name>.json`.
- Ensure `SecretsDir` in `Paths.Ensure()` with 0700 perms.
- New sentinels in `profile`, `secret`, `allowlist` packages (added in their respective units); wire their classifications in `ExitCodeFor` up front so later units don't need to touch this file.
  - `ErrMarkerNotAllowed` → `ExitConflict`
  - `ErrMarkerHashMismatch` → `ExitConflict`
  - `ErrSecretNotFound` → `ExitUser`
  - `ErrSecretRefUnresolved` → `ExitState`
  - `ErrAuditSecretsDetected` → `ExitConflict`
  - `ErrUnsupportedPlatform` (keyring on Windows) → `ExitState`
- Add rows to `exit_test.go` for each.

**Patterns to follow:** existing `paths.Paths` struct, existing `ExitCodeFor` table.

**Test scenarios:**
- Happy path: `Paths.Resolve()` returns the new fields derived from `CCP_ROOT`.
- Happy path: `Paths.Ensure()` creates `SecretsDir` with 0700.
- Happy path: `SecretFilePath("work")` returns the right path.
- Edge case: `SecretFilePath("")` returns empty-name-aware path (validation happens elsewhere).
- Integration: `exit_test.go` rows for each new sentinel return the expected code.

**Verification:** `go build ./...` green, every new sentinel present in `exit_test.go`, `SecretsDir` exists and has mode 0700 after `Ensure()`.

---

- [ ] **Unit 2: Reference parser and resolver (`internal/refs/`)**

**Goal:** Parse `{{ ... }}` templates and resolve them against a pluggable `Resolver` (keychain, op, env).

**Requirements:** R2, R5.

**Dependencies:** Unit 1.

**Files:**
- Create: `internal/refs/refs.go`
- Create: `internal/refs/refs_test.go`
- Create: `internal/refs/refs_windows.go` (build-tagged stub: `ErrUnsupportedPlatform`)

**Approach:**
- Public API: `refs.HasRefs(b []byte) bool`, `refs.HasAnyRefs(dir string) (bool, error)`, `refs.Render(ctx context.Context, b []byte, r Resolver) ([]byte, error)`, `refs.ParseRef(s string) (Ref, error)` where `Ref` is a tagged union (`RefKeychain`, `RefOp`, `RefEnv`).
- `Resolver` is an interface with `Resolve(ctx context.Context, ref Ref) (string, error)`. Composite `DefaultResolver{Profile string}` — profile name is resolver-scoped so `keychain:` refs resolve without a profile prefix.
- **Grammar** (profile-implicit, collision-safe):
  - `{{ keychain:<key> }}` → looks up `service="ccp"`, `account=<Resolver.Profile>`, `key=<key>`.
  - `{{ op://<vault>/<item>/<field> }}` → shells out to `op read`.
  - `{{ env.<NAME> }}` → `os.LookupEnv`.
  - Whitespace inside `{{ ... }}` is tolerated.
  - `{{!}}` immediately preceding a ref-shaped sequence makes the next `{{...}}` literal (the `{{!}}` itself is stripped from output).
- `HasRefs(b)` matches `{{\s*(keychain:|op://|env\.)` via a compiled regex — any `{{` that isn't one of these three schemes is NOT a ref, passes through verbatim, and does not trigger rendering. No parse error for unknown schemes — they simply don't exist.
- `HasAnyRefs(dir)` walks `dir`, running `HasRefs` on each regular file's bytes with early exit on first match. Used by `ccp exec` to decide whether to refresh.
- Malformed refs inside a recognized scheme (`{{ keychain: }}` with empty key; `{{ op:// }}` with no path) are a hard parse error with a byte-offset-aware message. This is intentional — tells the user their syntax is broken rather than silently treating it as literal.
- `opRead` is a package-level `var opRead = exec.Command` style function so tests can inject a fake — mirrors `internal/sync/auth.go:warnWriter` pattern.

**Execution note:** Implement with test-first for the parser — the grammar has enough edge cases (whitespace, malformed schemes, stray `{{`) that it pays off.

**Patterns to follow:** `internal/profile/alias.go` shows a clean mini-parser pattern; `internal/sync/auth.go:warnWriter` shows the package-level function var pattern for test overrides (use the same shape for `opRead` so tests can fake `op`).

**Test scenarios:**
- Happy: single `{{ keychain:KEY }}` resolves against the resolver's profile.
- Happy: multiple refs of different schemes in one file resolve in order.
- Happy: `{{ env.FOO }}` with `FOO` set resolves.
- Happy: `{{ op://vault/item/field }}` resolves via faked `opRead`.
- Edge: whitespace variations `{{  keychain:KEY  }}` parse.
- Edge: `{{` without closing `}}` passes through verbatim.
- Edge: `{{ .Values.foo }}` (Helm-style) passes through verbatim — not a recognized scheme.
- Edge: Markdown prose `Use \`{{ placeholder }}\` for...` passes through verbatim.
- Edge: `{{!}}{{ keychain:KEY }}` emits literal `{{ keychain:KEY }}` (escape works).
- Edge: file with no refs short-circuits via `HasRefs == false` (no `Render` call).
- Error: `{{ keychain: }}` (empty key) is a parse error with byte offset.
- Error: `{{ op:// }}` (empty path) is a parse error.
- Error: missing keychain entry → `ErrSecretRefUnresolved` with ref in message.
- Error: `op` TTY path with context-canceled (30s) → `ErrSecretRefUnresolved`.
- Error: `op` non-TTY + no service account → refuse with clear "set OP_SERVICE_ACCOUNT_TOKEN" hint.
- Error: `op` non-TTY + service account + timeout 5s → `ErrSecretRefUnresolved`.
- Integration: `Render` on a realistic `settings.json` with all three schemes resolves correctly.
- Integration: `HasAnyRefs(dir)` on a profile with one ref in `hooks/foo.sh` returns true with one file read.
- Integration: `HasAnyRefs(dir)` on a profile with no refs returns false after walking all files.

**Verification:** Every scheme resolves or fails with the right sentinel; grammar is table-driven; `HasRefs` is cheap enough to run on every `BuildSymlinks` iteration without benchmarked regression (qualitative check via unit test that does a length-1000 string without `{{` in under a millisecond).

---

- [ ] **Unit 3: Secret storage (`internal/secret/`)**

**Goal:** Keychain-first, file-fallback store for per-profile secrets, with CRUD + an `Entropy` scanner for reuse by audit.

**Requirements:** R1, R6.

**Dependencies:** Unit 1, Unit 2.

**Files:**
- Create: `internal/secret/secret.go`
- Create: `internal/secret/secret_test.go`
- Create: `internal/secret/secret_windows.go` (stub returning `ErrUnsupportedPlatform`)
- Modify: `go.mod`, `go.sum` (add `github.com/zalando/go-keyring`)

**Approach:**
- Public API: `Set(p paths.Paths, profile, key, value string) error`, `Get(p, profile, key) (string, error)`, `List(p, profile) ([]string, error)`, `Delete(p, profile, key) error`.
- Keychain-error discrimination helper: `classifyKeyringErr(err) (state keychainState)` returning one of `{Unavailable, Locked, NotFound, Other}`. Backends:
  - macOS: `security: SecKeychainItemCopyContent: The user name or passphrase you entered is not correct` → `Locked`. D-Bus absence on Linux → `Unavailable`. `keyring.ErrNotFound` → `NotFound`. Classification leans on error-string matching because `go-keyring` doesn't export typed sentinels for every case; conservative rules with a documented false-positive fallback to `Other`.
- Precedence:
  - `Set`: keychain first. On `Unavailable`, fall back to file store with one-time stderr warning. On `Locked`, return `ErrKeychainLocked` (do NOT fall back — the user explicitly has a keychain and it needs unlocking).
  - `Get`: keychain first. On `NotFound`, try file store (catches: user set on a keychain-unavailable machine, synced to a keychain-available machine). On `Locked`, return `ErrKeychainLocked`.
  - `Delete`: best-effort — remove from both keychain and file store.
- File store: JSON object `{key: value}`, atomic write via temp+rename inside `p.SecretsDir`, 0600 perms. Use a package-local helper; copy the pattern from `internal/manifest/manifest.go:Save`.
- Fallback warning: on first fall-through per process, print to stderr: `"keychain unavailable (<reason>); storing in <file path> — install libsecret/gnome-keyring for keychain-backed storage"`. Implement with a `sync.Once`.
- Test strategy: `keyring.MockInit()` at test start — every test runs hermetically. **No `t.Parallel()` in `internal/secret` tests** (MockInit is package-level state; comment documents this invariant).

**Patterns to follow:** `internal/manifest/manifest.go` atomic save; `internal/sync/auth.go:SetAuthWarnWriter` for the "first-time warning" redirection hook.

**Test scenarios:**
- Happy: `Set("work", "KEY", "val")` → `Get("work", "KEY") == "val"`.
- Happy: `Delete` removes from both backends; subsequent `Get` returns `ErrSecretNotFound`.
- Happy: `List("work")` returns keys alphabetically, merging keychain and file-store entries without duplicates.
- Integration: simulated keychain unavailable (`MockInitWithError(unavailable)`) → file fallback engages, warns exactly once, subsequent Set/Get go to file without re-warning.
- Integration: simulated keychain locked (`MockInitWithError(locked)`) → `Set` returns `ErrKeychainLocked`, `Get` returns `ErrKeychainLocked`, NO file fallback.
- Integration: machine A Sets to keychain; machine B (no keychain) Gets the same profile → hits `ErrSecretNotFound` (unless the user re-Sets locally).
- Edge: key validation rejects empty, slashes, colons (reserved for ref grammar), control chars, >255 chars.
- Edge: profile validation reuses `profile.ValidateName`.
- Edge: 1500-byte value on macOS mock → returns `ErrSetDataTooBig` with a clear message (don't try to chunk or workaround).
- Error: file-fallback write on read-only disk returns `fs.ErrPermission` through the chain.
- Error: fall-through never writes plaintext when profile name is invalid.
- Edge: file mode is 0600 after write; directory is 0700.
- Integration: two concurrent `Set` calls serialize correctly (exercises flock).

**Verification:** Hermetic tests pass with no real keychain access; every failure path returns the typed sentinel registered in Unit 1.

---

- [ ] **Unit 4: `BuildSymlinks` render-on-references integration + doctor + exec**

**Goal:** Make `BuildSymlinks` and `RefreshSymlinks` render reference-bearing files instead of symlinking; teach `Doctor` to stop warning about them; have `ccp exec` refresh before spawning.

**Requirements:** R2, R5.

**Dependencies:** Unit 2, Unit 3.

**Files:**
- Modify: `internal/profile/symlinks.go`
- Modify: `internal/profile/doctor.go`
- Modify: `internal/cli/exec.go`
- Modify: `internal/paths/paths.go` (add `RuntimeManifestDir`, `RuntimeManifestPath(profile)` → `<ConfigDir>/runtime-manifest/<profile>.json`; ensure the dir with 0700)
- Modify: `internal/profile/manager_test.go`
- Modify: `internal/profile/doctor_test.go`
- Modify: `internal/cli/m3_test.go` (add a ref-resolution end-to-end fixture)
- Create: `internal/profile/render.go` (extracted helper for the per-file render path; runtime manifest load/save)
- Create: `internal/profile/render_test.go`

**Approach:**
- New helper `renderFile(ctx, src, dst string, srcMode os.FileMode, resolver refs.Resolver) error` — reads source, calls `refs.Render`, writes to dst atomically via temp-in-same-dir+rename. **Mode preservation**: the rendered file's mode is `srcMode & 0755 | 0600` — clamps world-readability off, keeps the execute bit from source, always sets 0600 minimum. So a 0755 hook script with refs remains executable; a 0644 `settings.json` becomes 0600. Plain literal file writes get 0600.
- `BuildSymlinks`: within the `SharedItems` loop, dispatch based on item type AND current runtime state:
  - **File item, source has refs**: `renderFile`. If runtime path is currently a symlink, remove the symlink first.
  - **File item, no refs**: existing `ensureSymlink` path.
  - **Directory item**: pre-scan — does any file in the source tree match `refs.HasRefs`?
    - **No refs anywhere**: if runtime is a real dir from a prior state, walk it and remove only ccp-owned entries (symlinks into source + rendered regular files identifiable via a small signature — see "rendering identity" below), then replace with a single top-level symlink. If runtime is already a symlink, no-op.
    - **Refs somewhere**: if runtime is a top-level symlink to source, remove it; `MkdirAll` a real 0700 dir. Recurse through source entries — symlink non-ref files/dirs, render ref files. Subdirectories containing refs become real 0700 dirs and recurse.
- **Rendering identity** — how `RefreshSymlinks` knows which regular files in the runtime are ccp-owned vs Claude-written:
  - Track via a companion file `~/.config/ccp/runtime-manifest/<profile>.json` (new) listing rendered file paths relative to the runtime dir. Updated under flock whenever render writes complete.
  - Rationale: stateful, but avoids ambiguity. Alternative (byte-header stamp in rendered files) would be fragile and add noise to JSON/sh content.
- `ensureSymlink` invariant unchanged; `renderFile` adds a parallel invariant: refuses to overwrite a file not listed in the runtime manifest (protects Claude-created runtime files that happen to share a name).
- `Doctor` pass 1: when the runtime entry is a regular file and the source has refs, skip the warning. Pass 2 (new sub-pass): for each ref declared in source, call the resolver in a dry-run mode that returns whether the ref would resolve; surface unresolved refs as `warn` findings with a hint ("run `ccp secret set <profile> <key>` to fix").
- `ccp exec`:
  - Short-circuit via `refs.HasAnyRefs(pr.SourceDir)` — if no refs anywhere, skip the refresh and preserve the legacy fast path (pure env-var override).
  - If refs exist, acquire flock, call `pr.RefreshSymlinks()` (with context from a caller-supplied timeout — default 30s from main), propagate errors clearly.
  - `--no-refresh` flag bypasses even when refs exist (power-user escape hatch).
- **Partial-failure policy inside `BuildSymlinks`**: collect errors per file, continue the loop, return a joined error via `errors.Join` at the end. Successfully rendered files persist; failed files leave prior rendered content intact (the atomic rename only commits on success). Runtime manifest only records files that completed successfully.

**Execution note:** Write a failing integration test first (`settings.json` with a `keychain://` ref renders to the runtime dir with the value), then implement. The render path is subtle enough that TDD saves time.

**Patterns to follow:** existing `BuildSymlinks` / `ensureSymlink` style; `internal/manifest/manifest.go:Save` for atomic file writes.

**Test scenarios:**
- Happy: source `settings.json` with `{{ keychain:KEY }}` renders to `~/.claude-work/settings.json` with the stored value; mode is 0600.
- Happy: source hook script `hooks/on-start.sh` (0755) with a ref renders with mode 0700 (0755 & 0755 | 0600 → 0755 with world bits stripped to 0700 after 0600 floor — verify exact computation in tests).
- Happy: source with no refs still symlinks (byte-for-byte identical to pre-change behavior).
- Happy: mixed directory (`hooks/` with a ref file and a non-ref file) renders the ref file, symlinks the non-ref file, and leaves the directory as a real 0700 dir.
- Happy: `RefreshSymlinks` replaces stale rendered content when a keychain value changes (the source bytes didn't change, but the runtime manifest entry triggers a re-render).
- Happy: **symlink→real-dir transition** — profile starts with `hooks/` symlinked; user adds a ref to a file in `hooks/`; next `RefreshSymlinks` removes the symlink, materializes a 0700 dir, symlinks non-ref files, renders ref files. Runtime manifest updated.
- Happy: **real-dir→symlink transition** — user removes the last ref from files under `hooks/`; next `RefreshSymlinks` walks the runtime dir, removes ccp-owned symlinks and rendered files via runtime manifest, removes the now-empty dir, creates a top-level symlink. Runtime manifest entries cleared.
- Edge: rendered file mode is 0600 minimum (test `settings.json` at source 0644 → runtime 0600).
- Edge: rendered executable file preserves execute bit (test source 0755 → runtime 0700).
- Edge: runtime dir is 0700.
- Edge: `Doctor` no longer flags a rendered file as a warn ("expected symlink") when source has refs.
- Edge: `Doctor` still flags a regular file with no source refs as a warn (Claude overwrite case).
- Edge: `Doctor` pass 2 flags a missing keychain entry with a helpful `ccp secret set` hint.
- Error: `BuildSymlinks` with a ref that can't resolve (missing keychain entry) → continues rendering other files, returns a joined error naming the unresolved ref. Partial state consistent; the failed file is not half-written; other files render successfully.
- Error: two concurrent `ccp use` calls against a ref-bearing profile don't corrupt the runtime manifest (flock serializes).
- Integration: `ccp exec work -- /bin/cat ~/.claude-work/settings.json` prints the resolved value (CLI roundtrip under `setupCLI`).
- Integration: `ccp exec work -- /bin/true` on a profile with no refs does NOT call `RefreshSymlinks` (verify via a test hook that counts invocations).
- Integration: `ccp exec --no-refresh work -- /bin/true` on a ref-bearing profile skips refresh.
- Integration: rendering identity — user places a regular file named `settings.json` in `~/.claude-work/` outside of ccp; `BuildSymlinks` refuses to overwrite it (clear error "not a ccp-rendered file").

**Verification:** `BuildSymlinks` no longer leaks `{{ ... }}` strings into the runtime dir; `Doctor` is quiet on rendered files; `ccp exec` resolves refs before starting Claude.

---

- [ ] **Unit 5: `ccp secret` CLI (set / get / list / rm)**

**Goal:** Expose the `internal/secret` package as cobra commands, following the existing CLI patterns.

**Requirements:** R1, R6.

**Dependencies:** Unit 3.

**Files:**
- Create: `internal/cli/secret.go` (group)
- Create: `internal/cli/secret_set.go`
- Create: `internal/cli/secret_get.go`
- Create: `internal/cli/secret_list.go`
- Create: `internal/cli/secret_rm.go`
- Create: `internal/cli/secret_test.go`
- Modify: `internal/cli/root.go`
- Modify: `README.md` (add to the Commands section; add a "Secrets" subsection in the docs)

**Approach:**
- `newSecretCmd()` mirrors `newSyncCmd()` / `newProfileCmd()` — group with four child commands.
- `set <profile> <key> [value]`: if value is omitted and stdin is a TTY, refuse (match existing `profile delete` TTY discipline) and ask user to pass `--value`, pipe stdin, or use the `--stdin` flag. If `--stdin` is set, read from stdin.
- `get <profile> <key>`: prints value to stdout with no trailing newline (scriptable).
- `list <profile>`: prints keys one per line; `--json` returns `{profile: ..., keys: [...]}`.
- `rm <profile> <key>`: idempotent — `--yes` required only for bulk rm (not planned here; single-key rm is safe).
- All commands go through `withLockedState` to serialize with other ccp state mutations.

**Patterns to follow:** `internal/cli/profile_create.go` for arg parsing, `internal/cli/profile_list.go` for `--json` output, `internal/cli/profile_delete.go` for TTY / confirmation discipline.

**Test scenarios:**
- Happy: `secret set work KEY val` + `secret get work KEY` prints `val`.
- Happy: `secret list work --json` returns the expected JSON shape.
- Happy: `secret rm work KEY` then `secret get work KEY` exits with `ExitUser`.
- Edge: `secret set work KEY --stdin` reads from piped stdin.
- Edge: `secret set work KEY` (no value, no stdin, no TTY in test) returns an error mentioning `--value` or `--stdin`.
- Edge: invalid profile name is rejected with `ExitUser`.
- Edge: invalid key (`KEY WITH SPACES`) is rejected with a clear message.
- Integration: concurrent `secret set` calls don't corrupt the fallback file (under `MockInitWithError`).
- Integration: `secret set work KEY val`, then `ccp use work` renders a `settings.json` containing that `val` when the settings file has `{{ keychain://ccp/work/KEY }}`.

**Verification:** All four verbs work over an end-to-end CLI harness; no real keychain access in tests.

---

- [ ] **Unit 6: `ccp profile audit`**

**Goal:** Walk a profile's source tree and flag high-probability secret patterns so users can migrate them to `ccp secret set`.

**Requirements:** R4.

**Dependencies:** Unit 1, Unit 3 (reuse the entropy helper).

**Files:**
- Create: `internal/profile/audit.go`
- Create: `internal/profile/audit_test.go`
- Create: `internal/cli/profile_audit.go`
- Create: `internal/cli/profile_audit_test.go`
- Modify: `internal/cli/profile.go` (register the command)
- Modify: `README.md`

**Approach:**
- `profile.Audit(p paths.Paths, name string) ([]AuditFinding, error)`:
  - Walk `pr.SourceDir` (use `filepath.WalkDir`; mirror `diff.go`'s pattern).
  - For every regular file whose size is reasonable (< 1 MB — skip larger with a "file too large to scan" finding at info-level), scan each line for:
    - Prefix regexes: AWS access keys, GitHub classic + fine-grained PATs, Stripe live/test keys, Slack tokens, Google API keys, JWTs, PEM private key blocks.
    - Shannon entropy ≥ 3.5 over substrings ≥ 20 chars from a base64/hex charset.
  - Exclude anything that is itself a `{{ ... }}` ref (already safe).
  - Emit `AuditFinding{File, Line, Kind, Preview}` — preview redacted to first/last 4 chars.
- `ccp profile audit <name>`: prints a human summary (path:line [kind] preview); `--json` returns findings; nonzero exit if any findings (`ErrAuditSecretsDetected`).
- `allow` a unit test to plant an AWS-looking string and assert detection.

**Patterns to follow:** `internal/cli/profile_doctor.go` for the JSON-with-nonzero-exit pattern.

**Test scenarios:**
- Happy: profile with no secrets returns no findings, exit 0.
- Happy: profile with `AKIAIOSFODNN7EXAMPLE` flags AWS pattern.
- Happy: profile with a high-entropy base64 string ≥ 20 chars flags entropy finding.
- Happy: profile with `ghp_0123456789ABCDEF…` (36-char body) flags GitHub classic.
- Edge: a `{{ keychain:KEY }}` template is NOT flagged.
- Edge: a `{{!}}{{ keychain:KEY }}` escaped literal is NOT flagged (escape is respected by scanner).
- Edge: a long UUID (36 chars, low entropy) is NOT flagged by the entropy pass.
- Edge: a 64-char git SHA256 hash IS flagged (high entropy + hex charset); add an allowlist directive `# ccp:audit-ignore` the scanner respects.
- Edge: a 1.5 MB binary file is skipped with an info finding.
- Edge: `--json` output is valid JSON even on nonzero exit.
- Edge: `--json` output stable ordering (sorted by path + line).
- Error: missing profile → `ErrNotFound` → `ExitUser`.
- Integration: exit code 4 (`ExitConflict`) when findings exist.

**Verification:** Redaction preview never leaks >8 chars of a flagged secret; JSON shape matches `profile_doctor`'s convention.

---

- [ ] **Unit 7: `ccp profile export`**

**Goal:** Stream a portable tarball of a profile, stripping secrets by default.

**Requirements:** R3.

**Dependencies:** Unit 2, Unit 3 (read secrets file path / keychain), Unit 6 (reuse the audit scanner so export can refuse to ship a profile that still contains inlined secrets).

**Files:**
- Create: `internal/profile/export.go`
- Create: `internal/profile/export_test.go`
- Create: `internal/cli/profile_export.go`
- Create: `internal/cli/profile_export_test.go`
- Modify: `internal/cli/profile.go`
- Modify: `README.md`

**Approach:**
- `profile.Export(p paths.Paths, name string, opts ExportOptions, w io.Writer) error`.
- `ExportOptions{IncludeSecrets bool, FailOnAudit bool, SkipAudit bool}`.
- **Default behavior** (no `--include-secrets`):
  - Walk `pr.SourceDir`. Files with `{{ ref }}` syntax are emitted **verbatim** — refs are portable and the recipient resolves against their own keychain. No redaction needed because nothing sensitive is present.
  - Skip the per-profile `secrets/<name>.json` entirely.
  - Write `EXPORT_MANIFEST.json` with `contains_secrets: false` + file list.
- **`--include-secrets`**:
  - Resolve each ref via `refs.Render` using `DefaultResolver{Profile: name}` and inline the resolved value.
  - Include the `secrets/<name>.json` file verbatim.
  - TTY guard: CLI refuses `--include-secrets` when stdin is not a TTY unless `--yes-really` is also passed.
  - `EXPORT_MANIFEST.json` gets `contains_secrets: true`, `inlined_files: [...]`, timestamp, exporting hostname. A future `ccp profile import` can key off `contains_secrets` to re-confirm.
- **Audit gate is opt-in**:
  - `--fail-on-audit`: run `profile.Audit` before writing; refuse if findings with ExitConflict.
  - `--skip-audit`: don't run audit at all.
  - **Default**: run audit, print findings to stderr as a hint ("N suspected secrets found; review with `ccp profile audit`"), but proceed with the export. Entropy-based detectors have too many false positives to gate by default.
- CLI: `ccp profile export <name> [-o path]`; default writes tar to stdout; refuses if stdout is a TTY and `-o` is unset.

**Patterns to follow:** `internal/sync/sync.go:mergeCopyDir` for walking; `archive/tar` stdlib for writing.

**Test scenarios:**
- Happy: default export produces a tar; `{{ ref }}` syntax is preserved verbatim; no secrets file in tar.
- Happy: `--include-secrets` (with TTY fake / `--yes-really`) resolves refs inline; values present in the tar; `secrets/<name>.json` included.
- Happy: default audit finding is advisory (printed to stderr, exit 0).
- Happy: `--fail-on-audit` on a profile with AWS key → refuses with `ExitConflict`.
- Happy: `--skip-audit` skips scanning entirely.
- Edge: `-o /tmp/out.tar` writes to file; file perms 0600.
- Edge: export to TTY without `-o` refuses with a clear error.
- Edge: `EXPORT_MANIFEST.json` includes `contains_secrets` flag, exporter hostname, timestamp.
- Edge: `--include-secrets` without TTY and without `--yes-really` refuses.
- Edge: advisory audit stderr is suppressed with `-q` / `--quiet` flag (add if existing commands have one; check `internal/cli/*.go`).
- Error: missing profile → `ExitUser`.
- Error: `--include-secrets` with a missing keychain entry → `ExitState` with the unresolved ref in the message.
- Integration: tarball roundtrips through `tar -xf -` to the expected file tree.

**Verification:** No invocation of `profile export` ever emits a cleartext keychain value without a confirmation path.

---

- [ ] **Unit 8: Allow-list package (`internal/allowlist/`)**

**Goal:** Hash, store, load, approve, revoke, and check `.claude-profile` markers with TOCTOU-safe hashing under flock.

**Requirements:** R8, R9.

**Dependencies:** Unit 1.

**Files:**
- Create: `internal/allowlist/allowlist.go`
- Create: `internal/allowlist/allowlist_test.go`
- Create: `internal/allowlist/allowlist_windows.go` (stub)

**Approach:**
- `File` type mirroring `manifest.Manifest`: `SchemaVersion int`, `Entries map[string]string`. Atomic load/save with the same temp+rename pattern.
- `Hash(path string) (string, error)`: `os.OpenFile(path, O_RDONLY|O_NOFOLLOW, 0)` (reject symlinks), `fstat` for size sanity (<64KB), stream contents-only through `sha256.New()`. **Content-only hash** — no path prefix. Rationale: ccp's headline feature is sync-across-machines; including path in hash would require re-approval on every workstation with different user/path (detailed in Key Decision 12).
- `ReadName(path string) (string, error)`: reads the marker, validates format (single line matching `^[a-z][a-z0-9_-]{0,62}$` plus optional trailing `\n`, no BOM/CRLF/extra whitespace), returns profile name or typed error with byte-offset hint.
- `Approve(p paths.Paths, path string) error`: caller wraps in `withLock(p, fn)`; inside: load `allowlist.toml`, compute hash, upsert entry, atomic save.
- `Revoke(p paths.Paths, path string) error`: caller wraps in `withLock`; inside: load, remove entry, atomic save. Idempotent.
- `Check(p paths.Paths, path string) (Status, string, error)`: returns `StatusAllowed` / `StatusUnallowed` / `StatusHashMismatch` and the current on-disk hash for informational display. **Reads `allowlist.toml` WITHOUT acquiring the lock** — relies on atomic-rename semantics for consistency (readers see old or new, never torn). This is the hot-path discipline for `shell-resolve-dir` callers; codified as a public invariant in the package doc comment.
- Walk-up helper: `FindMarker(startDir string, homeDir string) (string, error)` — walks up from `startDir`, stopping at the first `.claude-profile`, at `homeDir`, or at the first `.git` directory (whichever is nearest). Returns `""` with nil error on not-found. Hard ancestry cap of 64 levels for defense in depth.

**Patterns to follow:** `internal/manifest/manifest.go` for file shape; `internal/profile/symlinks.go:rejectEscapingSymlinksInSource` for `O_NOFOLLOW` discipline.

**Test scenarios:**
- Happy: `Approve(path)` followed by `Check(path)` returns `Allowed`.
- Happy: edit the file's content → `Check` returns `HashMismatch`.
- Happy: move the file to a new path with identical content → `Hash` is identical (content-only hashing — test documents the deliberate tradeoff). A separate `Approve` entry is still needed because entries are path-keyed for lookup, but identical-content file at a new path would not need re-approval if the user also updates the key (out of scope; documented).
- Happy: `Revoke` removes the entry; `Check` returns `Unallowed`.
- Happy: `FindMarker` finds nearest marker walking up.
- Happy: `ReadName` on a valid single-line marker returns the name.
- Edge: symlink at the marker path is rejected (`O_NOFOLLOW` fires).
- Edge: BOM-prefixed marker → `ReadName` returns error naming byte offset 0 as invalid.
- Edge: CRLF-terminated marker → `ReadName` returns error; profile name would be `work\r` which fails validation.
- Edge: marker with trailing whitespace → `ReadName` rejects cleanly.
- Edge: multi-line marker → `ReadName` rejects (not "first non-empty line"; strict single line only).
- Edge: zero-width space prefix → rejected (regex only accepts `[a-z]` first char).
- Edge: >64KB marker → rejected by fstat size check.
- Edge: dangling path in allow-list doesn't crash `Check` (returns error-with-context).
- Edge: schema version mismatch refuses to load with an "upgrade ccp" message.
- Edge: `FindMarker` stops at `$HOME` and at the nearest `.git/` dir.
- Edge: `FindMarker` cap at 64 ancestors returns empty + nil.
- Integration: two concurrent `Approve` calls on different paths don't corrupt the file (flock serialization — verify by running them in goroutines and reading final state).
- Integration: `Check` reading `allowlist.toml` without the lock never sees a torn write (under a concurrent `Approve` loop, every `Check` sees either the old or new complete state).

**Verification:** TOCTOU window is closed (hash is computed from the same FD that fstat read); no Approve path ever follows a symlink; `Check` is lock-free and correct under concurrent writes.

---

- [ ] **Unit 9: `ccp allow` / `ccp deny` CLI**

**Goal:** Expose the allow-list operations with discoverable ergonomics.

**Requirements:** R7, R8, R9, R12.

**Dependencies:** Unit 8.

**Files:**
- Create: `internal/cli/allow.go`
- Create: `internal/cli/allow_test.go`
- Modify: `internal/cli/root.go`
- Modify: `README.md`

**Approach:**
- Three user-facing surfaces:
  - `ccp allow` (no args): from `$PWD`, walk up for `.claude-profile`; if found, hash and upsert entry. Print "Approved <path>" with the hash. Refuse if no marker is found.
  - `ccp allow --status`: walk up; print `Allowed` / `Unallowed` / `HashMismatch (approved hash ≠ current)` / `No marker found`, and exit code 0 for Allowed/None-found, 4 (`ExitConflict`) for Unallowed/Mismatch.
  - `ccp deny`: walk up; remove entry if present. Idempotent.
- All three acquire the global flock via `withLock`.
- Use `shellQuote` for any shell-visible output (future-proofing; current output is prose, no shell eval expected).

**Patterns to follow:** `internal/cli/profile_doctor.go` for summary prose + nonzero exit.

**Test scenarios:**
- Happy: in a dir with a `.claude-profile`, `allow` upserts; `allow --status` prints `Allowed`.
- Happy: edit the marker → `allow --status` prints `HashMismatch` and returns `ExitConflict`.
- Happy: `deny` clears the entry; `--status` becomes `Unallowed`.
- Happy: `deny` in a dir with no entry is a no-op (exit 0).
- Edge: no marker anywhere up the walk → `allow` errors with a clear hint; `--status` prints `No marker found` and returns 0.
- Edge: marker file is a symlink → refused by the hash layer.
- Edge: malformed marker (BOM, CRLF, multi-line, trailing whitespace) → `allow` refuses with a clear byte-offset error; `--status` prints the error and returns `ExitConflict`.
- Integration: hermetic test under `setupCLI`, with markers in a synthetic directory tree.

**Verification:** Users can approve, check, revoke, and re-approve a marker without ever touching the file directly.

---

- [ ] **Unit 10: Hidden `ccp shell-resolve-dir <dir>` helper**

**Goal:** Silent, fast, exit-0-always hot-path command the shell hook can call on cache miss.

**Requirements:** R7, R9, R11.

**Dependencies:** Unit 8.

**Files:**
- Create: `internal/cli/shell_resolve_dir.go`
- Create: `internal/cli/shell_resolve_dir_test.go`
- Modify: `internal/cli/root.go` (register as `Hidden: true`)

**Approach:**
- Signature: `ccp shell-resolve-dir <dir>`. Hidden from help.
- Steps: `paths.Resolve()` (no `Ensure`), `allowlist.FindMarker(dir, p.Home)`, if found: read first non-empty line, `profile.ValidateName`, `allowlist.Check(path)`; on `Allowed` with matching hash: print three lines — `CCP_AUTO_PROFILE=<name>`, `CCP_AUTO_MARKER=<path>`, `CCP_AUTO_MARKER_MTIME=<unix>`. All emitted with `shellQuote` applied to the value.
- All error paths return nil (exit 0). On `HashMismatch` or `Unallowed`, print ONE extra line: `CCP_AUTO_WARN=drift` or `CCP_AUTO_WARN=unallowed`; the shell hook decides whether to emit a stderr warning based on the per-marker `CCP_AUTO_WARNED_<hash>` guard.
- Maximum walk depth is implicit (FindMarker stops at `$HOME` / `.git`), but add a hard cap of 64 ancestors for defense in depth.

**Patterns to follow:** `internal/cli/shellactive.go` — the "never error on the hot path" discipline is the governing spec.

**Test scenarios:**
- Happy: allowed marker → expected three-line output.
- Happy: unallowed marker → `CCP_AUTO_WARN=unallowed` only; no `CCP_AUTO_PROFILE` line.
- Happy: mismatch → `CCP_AUTO_WARN=drift`.
- Happy: no marker anywhere → empty stdout, exit 0.
- Edge: invalid profile name in marker → empty stdout, exit 0 (fail closed; no CCP_AUTO_WARN because the marker itself is malformed).
- Edge: **symlinked marker → empty stdout, exit 0** (O_NOFOLLOW fires; silent skip is the documented invariant to prevent creating an existence oracle).
- Edge: marker path contains shell metacharacters (spaces, single quotes, `$`, backtick, newline, backslash) → `shellQuote` escapes correctly; eval'd output parses in `/bin/sh -c`.
- Edge: nonexistent `<dir>` argument → empty stdout, exit 0 (never error on hot path).
- Edge: allowlist.toml missing → empty stdout, exit 0.
- Edge: allowlist.toml unreadable (permissions changed) → empty stdout, exit 0. **Diagnosis**: users who run `ccp allow --status` get the real error; hot path never surfaces it.
- Integration: wall-clock timing via `time.Now()` subtraction under a unit test — first invocation timing target is qualitative ("<200ms on populated tree"); Go cold start dominates and is platform-dependent, so no hard threshold.
- Integration: table-driven `shellQuote` corpus with 20+ metacharacter combinations.

**Verification:** The command never emits stderr; exit code is 0 in every test; every output line is safe to `eval` in sh/bash/zsh/fish; symlinked markers silently skip without leaving a diagnostic trail an attacker could use.

---

- [ ] **Unit 11: Shell-init snippet extension**

**Goal:** Teach `ccp shell-init` to emit the auto-activation layer so `cd` triggers profile resolution fail-closed.

**Requirements:** R7, R10, R11, R12.

**Dependencies:** Unit 10.

**Files:**
- Modify: `internal/cli/shellinit.go`
- Modify: `internal/cli/shellinit_test.go`

**Approach:**
- Extend the POSIX `__ccp_activate` function:
  1. Existing escape hatch: `[ -n "$CLAUDE_CONFIG_DIR" ] && return 0`.
  2. New escape hatch: `[ "$CCP_PROFILE_AUTO" = "0" ] && { [set CLAUDE_CONFIG_DIR from CCP_PROFILE legacy path]; return; }`.
  3. Walk up for `.claude-profile` in pure shell (bounded, `while [[ ! -e ... && $PWD != / ]]; cd ..` surrogate with a `cwd` variable — do not actually mutate the user's `$PWD`).
  4. If not found: unset the auto cache vars; fall through to existing `CCP_PROFILE` logic.
  5. If found: compare `<path>` and mtime against `$CCP_AUTO_MARKER` / `$CCP_AUTO_MARKER_MTIME`. On hit, reuse `$CCP_AUTO_PROFILE`. On miss, `eval "$(ccp shell-resolve-dir "$current_dir")"` and update cache.
  6. If `CCP_AUTO_PROFILE` is set after that: `export CCP_PROFILE="$CCP_AUTO_PROFILE"` and fall through to existing logic.
  7. If `CCP_AUTO_WARN` is non-empty and the per-marker guard hasn't printed yet: write one-liner warning to stderr; set the guard var.
- `chpwd` hook for zsh; `PROMPT_COMMAND` append for bash; `--on-variable PWD` for fish. Each shell gets its own snippet but the same logic.
- Update the fish variant symmetrically.
- Markers stable (`shellInitBegin`/`shellInitEnd` unchanged) so a user's shellrc doesn't need edits — re-running `eval "$(ccp shell-init zsh)"` in a new shell picks up the new snippet.

**Patterns to follow:** existing `writePosix` / `writeFish` emit patterns; `TestShellInitPosixActuallyRunsInSh` as the integration harness.

**Test scenarios:**
- Integration (live `sh`): marker in a subdir, allowed → `CLAUDE_CONFIG_DIR` is set.
- Integration: marker content drift → no activation; stderr contains a warning with a `ccp allow --status` hint; second `cd` into the same dir does NOT re-warn (per-marker guard works).
- Integration: opening a fresh shell re-warns (guard is per-shell-pid via env var).
- Integration: no marker anywhere → `CLAUDE_CONFIG_DIR` is empty; legacy `CCP_PROFILE` escape hatch still works.
- Integration: `CLAUDE_CONFIG_DIR` already set before snippet runs → untouched.
- Integration: `CCP_PROFILE_AUTO=0` → auto layer skipped; legacy path still works.
- Integration: cache hit fast path — pre-set the cache env vars; assert `ccp shell-resolve-dir` is not called (verify via a `PATH` where `ccp` is a shim script that records invocations).
- Integration: negative cache semantics — `cd ~/code/` (no marker), cache entry. `cd ~/code/subdir-with-marker/` → cache MISS (descendant of no-marker dir but a new marker could exist). `cd ~/code/notes/` → cache HIT (sibling of the subdir, but still descendant of the cached no-marker root).
- Integration: marker deletion invalidates cache — `cd` after `rm .claude-profile` re-walks.
- Integration: `ccp exec` on a drifted-marker dir detects the mismatch between the active profile and the auto-resolved profile, emits a short advisory.
- Edge: fish snippet parses under `fish -c 'source /dev/stdin'` in CI where fish is available.
- Edge: snippet contains no `awk` (regression for existing `TestShellInitPosixContainsMarkersAndGuard`).
- Edge: marker path with single quotes, spaces, backtick, newline, dollar — the eval'd output evaluates correctly (shellQuote correctness across metacharacters).
- Edge: hook performance — `ccp shell-resolve-dir` is invoked at most once per unique `$PWD` change (no invocation at all on cache hit).

**Verification:** The `TestShellInitPosixActuallyRunsInSh` harness is extended with all acceptance-criteria scenarios from R7–R12. The hot-path shell-only branch runs without forking `ccp` when the cache is warm.

---

- [ ] **Unit 12: Sync gitignore, migration, README, docs**

**Goal:** Wrap up — gitignore allowlist, add first-run migration aid, update README.

**Requirements:** R6 (verify), general documentation, post-upgrade UX.

**Dependencies:** Units 1–11.

**Files:**
- Modify: `internal/sync/sync.go` (add `/allowlist.toml` and `/runtime-manifest/` to `GitignoreContents`)
- Modify: `internal/sync/sync_test.go` (assert both are gitignored)
- Modify: `internal/cli/sync_push.go` (run advisory `profile.Audit` before push; print stderr hint if findings)
- Modify: `internal/cli/profile_use.go` (first-run migration advisory on first post-v2 `ccp use`)
- Modify: `internal/manifest/manifest.go` (add `LastSeenVersion string` field — used to detect first post-v2 run; schema stays at 1, field is optional)
- Modify: `README.md` (flip v2 rows under Roadmap and Relationship-to-jean-claude; add Commands section entries; add "Secrets" + "Auto-activation" quickstart; add "Gotchas" subsection; add "Deviations from linked issues" paragraph at end of each feature section)

**Approach:**
- Gitignore: add `/allowlist.toml` and `/runtime-manifest/` to `GitignoreContents` between `/lock` and `/secrets/`; update the gitignore test.
- **Migration aid**:
  - `manifest.Manifest` gains `LastSeenVersion string` (omitempty). On every `ccp use`, compare against the current binary's build version; if they differ, emit a one-time stderr message: "ccp upgraded from <X> to <Y>; run `ccp profile audit` across your profiles to check for cleartext secrets you can migrate to keychain storage."
  - On an empty `LastSeenVersion` (first run post-v2) AND any profile file containing `ghp_`/`sk_live_`/`AKIA` patterns, print a short advisory with a pointer to `ccp profile audit`.
  - Update `LastSeenVersion` after the advisory fires.
- **Sync push audit**: `ccp sync push` runs `profile.Audit(p, manifest.ActiveProfile)` in advisory mode; findings print to stderr as a warning ("N suspected secrets in this profile; review with `ccp profile audit`"). Never blocks push. `--quiet` suppresses the advisory.
- README: move v2.0 roadmap items from "Planned" to "Shipped" form; add new commands; document `CCP_PROFILE_AUTO` escape hatch, rendering semantics, content-only hashing rationale, devcontainer caveats.
- **"Gotchas" subsection** must include: keychain unavailable on headless Linux/devcontainers, `op` biometric prompting, allow-list is per-machine (not synced by design), content-only hash semantics, shell hook latency on cold Go cache, `{{!}}` escape for literal refs.

**Patterns to follow:** existing README style (tables, concise bullets).

**Test scenarios:**
- Edge: `sync_test.go` asserts `GitignoreContents` contains `/allowlist.toml` and `/runtime-manifest/`.
- Edge: `sync setup` produces a `.gitignore` with all new entries.
- Happy: fresh `manifest.toml` without `LastSeenVersion` triggers migration advisory on first `ccp use`; second `ccp use` does not re-emit.
- Happy: `sync push` on a profile with `AKIA...` string emits stderr advisory; push proceeds.
- Edge: `sync push --quiet` suppresses the advisory.

**Test expectation: none for README diffs — reviewed manually.**

**Verification:** `git status` after a `ccp sync setup` on a fresh home shows neither `allowlist.toml` nor `secrets/` nor `runtime-manifest/` as staged; README commands table and roadmap reflect reality; users upgrading from v1 see the migration advisory exactly once.

## System-Wide Impact

- **Interaction graph:** `BuildSymlinks` is now a fan-out over `SharedItems` that forks on reference presence; `Doctor` now depends on reading source bytes to decide severity; `ccp exec` now triggers a `RefreshSymlinks` on every invocation; `shell-init` now forks an additional hidden command on cache miss; `sync push` / `sync pull` are unaffected beyond gitignore additions.
- **Error propagation:** every new sentinel registers in `ExitCodeFor` in Unit 1; agents using exit codes to branch should see `ExitConflict` on allow/deny hash mismatches and audit findings, `ExitState` on keychain unavailability and unresolved refs. No existing sentinel changes class.
- **State lifecycle risks:** partial renders during `BuildSymlinks` are the main worry — mitigated by writing each rendered file via atomic temp+rename, so a failed render leaves the previous rendered file intact and the runtime dir consistent. The audit scanner is purely read-only. The allow-list writes go through the global flock, matching manifest.
- **API surface parity:** nothing. All new surfaces are CLI and new internal packages. No existing public type changes shape.
- **Integration coverage:** Units 4 and 11 each have an end-to-end integration test (ref rendering via `ccp exec`, shell-init via `/bin/sh` eval). Unit 5 covers the CLI-roundtrip for secrets. That's enough surface to catch the cross-layer failure modes unit tests miss.
- **Unchanged invariants:** `~/.claude/` is still never touched; the three-tier activation precedence (`CLAUDE_CONFIG_DIR` → `CCP_PROFILE` → `manifest.ActiveProfile`) is preserved with the new auto layer sitting BETWEEN the env and the manifest (it sets `CCP_PROFILE`, then the existing logic consumes it). `sync push` still never touches machine-local state. Exit codes 0–4 retain their meanings; no new exit code is introduced.

## Risks & Dependencies

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Headless Linux / devcontainer keyring failure surprises users | High | Low | Loud one-time warning on first fallback; file store is 0600 + gitignored; README Gotchas documents the flow. |
| `op` CLI prompts interactively and hangs | Medium | High | Context-aware timeouts (30s TTY, 5s non-TTY+service-account, refuse otherwise); shell hook never resolves refs. |
| Keychain locked vs not-found confusion | Medium | High | Typed sentinels (`ErrKeychainLocked`, `ErrSecretNotFound`, `ErrKeychainUnavailable`) with distinct error messages and exit classes. |
| Shell hook latency exceeds budget on cold Go start | Medium | Low | Shell-only cache keeps hot path <5ms; cold path documented as 30–80ms (sometimes up to 150ms on Apple Silicon). Acceptance is qualitative, not a hard threshold that would flake CI. |
| Directory mode transitions (symlink↔real dir) leave runtime in inconsistent state | Medium | Medium | Unit 4 explicitly covers both transitions with runtime-manifest tracking; tests exercise symlink→dir→symlink roundtrip. |
| Users copy profiles across machines and forget to re-populate keychain entries | High | Low | `ccp profile doctor` pass 2 surfaces unresolved refs with a `ccp secret set` hint (Unit 4 scope). |
| Allow-list TOCTOU between hash and check | Low | High | `O_NOFOLLOW` open + hash-by-fd; path never re-resolved. |
| Content-only hash allows cross-machine sync but weakens direnv's "move attack" defense | Low | Low | Marker content is just a profile name, validated; the profile must already exist locally before refs matter. Documented tradeoff in README + Key Decision 12. |
| Users who had inlined secrets pre-v2 don't know to run `ccp profile audit` | High | High | First-run migration advisory on `ccp use`; `sync push` runs advisory audit. Plan Unit 12 delivers both. |
| `tar` streaming with `--include-secrets` leaks plaintext | Medium | High | TTY confirmation; `--yes-really` for non-TTY; `EXPORT_MANIFEST.json` marks `contains_secrets: true` for future `import` re-confirm. Document as a known footgun. |
| Reference delimiter `{{ ... }}` collides with user prose (Helm, docs) | High | Medium | `HasRefs` matches scheme signatures only; unknown `{{` passes through verbatim; `{{!}}` escape for literal refs. |
| Windows users try v2.0 features | Medium | Low | Commands unregistered on Windows — `unknown command` (ExitUser), not runtime error. Pointer to issue #6 in README. |
| Audit entropy detector produces false positives, users disable it | Medium | Medium | Regex prefilter + entropy confirmation (≥30 chars, ≥4.0 on base64/hex); export audit gate is opt-in (`--fail-on-audit`), not default. |
| Shell hook eval of `shell-resolve-dir` output becomes RCE surface | Low | Critical | `shellQuote` on every value; profile-name regex validation; `CCP_AUTO_WARN` values are constants. Tests cover metacharacter paths. Future maintainers warned via doc comment that the invariant is load-bearing. |

## Documentation / Operational Notes

- README gets: new commands, secrets quickstart, auto-activation quickstart, `CCP_PROFILE_AUTO` documented, Roadmap table updated, Relationship-to-jean-claude table updated.
- Exit-code table in README is refreshed with the new sentinels (still within codes 0–4 — no new top-level code is introduced; existing classification suffices).
- Gotchas subsection: headless Linux keyring, `op` biometric prompt, hash re-approval on marker edit, `CCP_PROFILE_AUTO=0` escape hatch, Windows deferral.
- No migration needed: existing profiles, manifest, and sync layout are fully forward-compatible.

## Deviations from Linked Issues

The plan diverges from the issue specs in a few places where review surfaced gaps. These are deliberate and should be noted when the issues are updated post-ship:

- **Allow-list format**: issue #4 specifies `~/.config/ccp/allowed.json` with `{ "/abs/path": "<hash>" }`. Plan uses `allowlist.toml` with a schema-versioned `[entries]` table. Rationale: matches ccp's existing `manifest.toml` conventions and leverages existing `fslock` + atomic-save primitives.
- **Hash scheme**: issue #4 says "hashes the current `.claude-profile` (SHA-256)". Plan hashes contents only (not `path + contents` as direnv does). Rationale: ccp's "sync across machines" tagline would break with path-in-hash (every workstation with different `$HOME` would require re-approval). Threat model analysis in Key Decision 12.
- **Reference syntax**: issue #5 specifies `{{ keychain://ccp/<profile>/<key> }}`. Plan uses profile-implicit `{{ keychain:<key> }}` (service/account inferred from the resolver's active profile). Rationale: refs in a `settings.json` shouldn't hard-code the profile name — it breaks on rename/copy/export-import.
- **Export audit gate**: issue #5's acceptance criteria don't specify behavior on audit findings; plan initially proposed gating by default, revised to opt-in (`--fail-on-audit`) because Shannon-entropy false positives would block legitimate exports.
- **`--yes-really` flag on `export --include-secrets`**: adds a non-TTY opt-in above and beyond issue #5's "prompts interactively" wording. Supply-chain defense-in-depth; prevents scripts from silently inlining secrets.
- **`--no-refresh` on `ccp exec`**: not in either issue. Added to preserve the legacy fast path for power users whose profiles have no refs or who don't want per-invocation I/O.
- **Exec refresh is conditional**, not unconditional: only fires when `refs.HasAnyRefs(pr.SourceDir)` — profiles without any refs are zero-cost. Issue #5 doesn't specify exec semantics; this was a derived decision.
- **First-run migration advisory + `sync push` advisory audit**: not in either issue. Added so existing users with inlined secrets get a clear path to remediation without reading release notes.
- **Windows command registration**: issue #5 defers Windows to v2.1 "if Windows support slips". Plan unregisters the commands on Windows entirely (not runtime `ErrUnsupportedPlatform`). Cleaner UX and correct exit-code classification for agents.
- **`.claude-profile` format**: issue #4 says "one-line file … containing just the profile name." Plan tightens this to reject BOM, CRLF, extra whitespace, and multi-line content with byte-offset diagnostics.
- **`{{!}}` escape sequence**: not in issue #5; added so users can write documentation that references the syntax without triggering the renderer.

## Sources & References

- Issue: [#4 — Auto-activation via `.claude-profile` marker with direnv-style allow-list](https://github.com/dalley/ccp/issues/4)
- Issue: [#5 — Secrets separation + OS keychain and `op://` reference resolution](https://github.com/dalley/ccp/issues/5)
- In-repo research: `.context/compound-engineering/ce-review/20260423-150232-25229e66/` (adversarial.json, security.json, reliability.json, correctness.json).
- External: [zalando/go-keyring](https://github.com/zalando/go-keyring) — Go keychain library with `MockInit()` testing hook.
- External: [direnv(1) security model](https://direnv.net/man/direnv.1.html) — allow-list hashing reference.
- External: [1Password CLI `op read`](https://developer.1password.com/docs/cli/reference/commands/read/) — command reference.
- External: [gitleaks default rules](https://github.com/gitleaks/gitleaks/blob/master/config/gitleaks.toml) — regex + entropy reference for the audit detector.
- Internal code references:
  - `internal/cli/shellinit.go`, `internal/cli/shellactive.go` — hook templates.
  - `internal/profile/symlinks.go`, `internal/profile/doctor.go` — `BuildSymlinks` and doctor plug points.
  - `internal/cli/exec.go` — exec integration point.
  - `internal/manifest/manifest.go` — atomic-save template.
  - `internal/cli/state.go` — `withLockedState` / `withLock` helpers.
  - `internal/cli/exit.go` — `ExitCodeFor` registration.
  - `internal/cli/m3_test.go`, `internal/cli/security_test.go`, `internal/cli/shellinit_test.go` — test harness templates.
