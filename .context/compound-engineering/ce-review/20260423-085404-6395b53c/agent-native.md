# Agent-Native Architecture Review: ccp CLI

## Summary

`ccp` is a Go CLI (Cobra-based) for managing multiple named Claude Code profiles on one machine. It targets both human users in a terminal and coding agents (Claude Code, Codex, Cursor) that need to read and modify Claude Code's own configuration. The agent-integration story here is inverted from a typical web app: there is no LLM backend, no system prompt, no tool registry -- ccp IS the tool layer that an agent calls via subprocess. The correct evaluation frame is therefore pure scriptability: can an agent invoke every command unambiguously, parse every output, and handle every outcome without human presence?

Overall verdict: the core profile CRUD is agent-accessible and the --yes escape hatch exists on delete, but five commands that agents routinely need return human-only text output with no --json flag, exit codes collapse to a single nonzero value so agents cannot distinguish between "profile not found" and "lock contention", and profile-name argument completion is absent from the shell completion scripts, forcing agents to call `profile list` before every command that takes a name.

---

## Capability Map

| CLI Action | File | --json? | --yes / --force escape? | Distinct exit code? | Agent status |
|---|---|---|---|---|---|
| profile list | profile_list.go | YES | n/a | YES (exit 0 = ok) | OK |
| profile show | profile_show.go | NO | n/a | NO (same exit 1 for all errors) | WARNING |
| profile create | profile_create.go | NO | n/a | NO | OBSERVATION |
| profile use | profile_use.go | NO | n/a | NO | WARNING |
| profile delete | profile_delete.go | NO | YES (--yes) | NO | WARNING |
| profile rename | profile_rename.go | NO | n/a | NO | OBSERVATION |
| profile diff | profile_diff.go | NO | n/a | NO | CRITICAL |
| profile doctor | profile_doctor.go | NO | n/a | partial (nonzero on error) | WARNING |
| profile rollback | profile_rollback.go | NO | NO (no confirmation, no --yes) | NO | OBSERVATION |
| profile refresh | profile_refresh.go | NO | n/a | NO | OBSERVATION |
| sync status | sync_status.go | NO | n/a | NO | CRITICAL |
| sync push | sync_push.go | NO | n/a | NO | WARNING |
| sync pull | sync_pull.go | NO | YES (--force) | NO | WARNING |
| sync setup | sync_setup.go | NO | n/a | NO | OBSERVATION |
| current | profile_list.go | NO | n/a | NO (exit 0 even when nothing active) | WARNING |
| use (shortcut) | profile_use.go | NO | n/a | NO | WARNING |
| exec | exec.go | n/a | n/a | YES (propagates child code) | OK |
| version | version.go | NO | n/a | YES | OBSERVATION |
| init | init.go | NO | n/a | YES | OBSERVATION |
| shell-init | shellinit.go | n/a | n/a | YES | OK |
| prompt | prompt.go | n/a | n/a | YES (always exit 0) | OK |
| completion zsh/bash/fish | cobra builtin | n/a | n/a | YES | PARTIAL (no profile name completions) |

---

## Findings

### Critical (Must Fix)

**1. `profile diff` has no machine-readable output mode -- agents cannot parse results**
`/Users/dalley/dev/Personal/ccp/internal/cli/profile_diff.go`

The diff command returns human text like `"  ~ changed  settings.json"` with embedded spaces and a string-formatted marker that varies by profile name (`"- only in work"`). The underlying `DiffEntry` struct already carries `Kind` (a typed string constant), `Path`, `HashA`, and `HashB` fields -- everything needed for a clean JSON document -- but none of it is surfaced to a `--json` consumer. An agent that wants to detect whether two profiles differ on a specific file must screen-scrape. The `DiffKind` constants (`only-in-a`, `only-in-b`, `changed`, `type-mismatch`) are stable and correct; they just need to be emitted.

Fix: add `--json` flag; emit `[]{"path":..., "kind":..., "hash_a":..., "hash_b":...}`. Exit 0 when identical, exit 1 when differences exist, exit 2 on error (see exit code finding below).

**2. `sync status` has no machine-readable output mode -- agents cannot branch on sync state**
`/Users/dalley/dev/Personal/ccp/internal/cli/sync_status.go`

`sync status` is the command an agent calls before deciding whether to push or pull. The output is a multi-line human label block (`Repo:`, `Branch:`, `Remote:`, `Status:`). The `StatusSummary` struct in `/Users/dalley/dev/Personal/ccp/internal/sync/sync.go:349` already carries `RepoExists`, `Dirty`, `ChangedFiles` (a `[]string`), `CurrentBranch`, and `Remote` as typed fields. None of that structure is visible to a `--json` consumer. An agent cannot programmatically know whether the repo is dirty without parsing human prose.

Fix: add `--json` to `newSyncStatusCmd()`; marshal the `StatusSummary` struct directly.

---

### Warnings (Should Fix)

**3. All errors collapse to exit code 1 -- agents cannot distinguish failure modes**
`/Users/dalley/dev/Personal/ccp/cmd/ccp/main.go:13`

`main()` unconditionally calls `os.Exit(1)` whenever `Execute()` returns a non-nil error. Every failure -- profile not found, manifest corrupt, lock contention, git push failure, invalid profile name, missing argument -- exits with the same code 1. An agent retrying after a lock contention must not retry the same way after a "profile not found" error, but currently the exit code gives no signal. Cobra's `SilenceErrors: true` in `root.go:14` means the error text goes to stderr only if the top-level `main` prints it, which it does -- but text parsing is fragile.

Recommended exit code scheme:
- 0: success
- 1: user/argument error (profile not found, invalid name, missing arg)
- 2: I/O or state error (manifest corrupt, disk full, lock fail)
- 3: network/git error (push/pull/clone failures)
- 4: conflict (profile already exists, working tree dirty)

This requires a small typed-error layer in the CLI layer and a switch in `main()`.

**4. `profile show` has no machine-readable output mode**
`/Users/dalley/dev/Personal/ccp/internal/cli/profile_show.go`

`profile show` prints a formatted table of item statuses (`present`, `absent`, `present (N entries)`). An agent checking whether a specific item (e.g., `settings.json`) is present in a profile must parse that text. The underlying data is trivially serializable: profile name, active bool, source dir, config dir, and a map of item name to status string.

Fix: add `--json`.

**5. `current` exits 0 and prints nothing when no profile is active -- agents cannot distinguish "no profile" from "command failed"**
`/Users/dalley/dev/Personal/ccp/internal/cli/profile_list.go:57`

```go
if s.Manifest.ActiveProfile == "" {
    return nil
}
```

An agent calling `current` to check the active profile receives an empty string on stdout and exit code 0 in both the "no profile active" and the "success, profile name printed" case. The only reliable path is `profile list --json` and inspecting the `active` field there. `current` should either exit with a distinct non-zero code when no profile is set, or add a `--json` flag that emits `{"active": null}` vs `{"active": "work"}`. The former is simpler and consistent with how Unix tools like `git branch --show-current` behave.

**6. `profile doctor` has partial machine-readability -- severity levels are not extractable**
`/Users/dalley/dev/Personal/ccp/internal/cli/profile_doctor.go`

`doctor` does exit non-zero when there are errors (`errorsFound > 0`), which is good. But warnings (severity == "warn") are silently included in text output and cause exit 0. An agent cannot distinguish "healthy", "has warnings", and "has errors" from exit code alone. Additionally there is no `--json` flag: the `DoctorFinding` struct already has `Profile`, `Severity`, `Message`, and `Hint` fields.

Fix: add `--json`; consider exit 0 / 1 / 2 for healthy / warnings-only / errors.

**7. `sync pull` without `--force` may block or fail with an opaque error when the working tree is dirty -- no machine-readable signal**
`/Users/dalley/dev/Personal/ccp/internal/sync/sync.go:287`

When the working tree is dirty and `--force` is not passed, Pull returns `fmt.Errorf("working tree has uncommitted changes; commit them (ccp sync push), or re-run with --force to discard")`. This is a human-readable string embedded in a generic error. An agent cannot distinguish this from a network failure or a git corruption. The string is not a typed sentinel error. An agent script doing `if ccp sync pull; then ...; fi` will silently conflate a dirty-tree refusal with a real failure.

Fix: define `var ErrDirtyWorkingTree = errors.New("dirty working tree")` in the sync package and wrap it so callers (and ultimately a distinct exit code) can detect it with `errors.Is`. Pair with exit code 4 (conflict) from finding 3.

**8. `sync push` has no dry-run output that is machine-readable**
`/Users/dalley/dev/Personal/ccp/internal/cli/sync_push.go`

`--dry-run` prints `"No local changes to commit (dry-run)."` or `"Dry-run: would push origin."` as human text. An agent using dry-run to decide whether a push is needed cannot reliably parse these strings. Fix: add `--json` to emit `{"would_commit": bool, "would_push": bool, "remote": "..."}`.

---

### Observations

**9. `profile rollback` has no --yes flag and no confirmation prompt -- behavior is asymmetric with delete**
`/Users/dalley/dev/Personal/ccp/internal/cli/profile_rollback.go`

`profile delete` guards against accidental destructive action with `confirm()` unless `--yes` is passed. `profile rollback` restores the most recent backup (a write operation that may overwrite existing profile state) with no confirmation at all. This is inconsistent. For agents, no prompt is actually preferred -- but a human running rollback accidentally gets no safety gate. Low priority, but worth noting.

**10. Cobra's `completion` command produces scripts with no profile-name completions**
The completion scripts (auto-generated by Cobra) complete subcommand names and flags but not profile names in argument position. Every command that takes `<name>` (`use`, `show`, `delete`, `rename`, `diff`, `exec`, `doctor`, `refresh`) would benefit from `ValidArgsFunction` returning the live profile list via `profile.List()`. For agents this is less critical (they call `profile list --json` instead) but it is the primary discovery surface for humans and makes the CLI feel unfinished.

**11. The fslock blocks indefinitely with no timeout**
`/Users/dalley/dev/Personal/ccp/internal/fslock/fslock.go:25`

`unix.Flock(fd, LOCK_EX)` blocks until the lock is acquired with no timeout. Two concurrent ccp invocations will serialize correctly, but a crashed process holding the lock will deadlock all subsequent invocations. An agent in a CI environment that kills a ccp process mid-run will leave the lock file held by the OS until the FD is closed -- which the OS does on process exit. In practice the OS releases flock on process death, so this is only a real problem if the lock file descriptor leaks into a child process that outlives ccp (unlikely but possible via `ccp exec`). The `exec.go` child process is started with the parent's inherited FDs, which includes the open lock FD. Add `syscall.FD_CLOEXEC` to the lock file open flags to close it in the child.

**12. Backup paths in `profile delete` and `sync pull` output include timestamps that vary by second**
`/Users/dalley/dev/Personal/ccp/internal/backup/backup.go:24`

The format `2006-01-02T15-04-05` (second precision) means two backups created in the same second will collide (the second `New()` call will silently return the same directory because `os.MkdirAll` succeeds on an existing dir). For agents running tests or automation rapidly this is a latent bug. Use nanoseconds or add a suffix like a random 4-hex string.

**13. `profile create` has no structured success output for automated workflows**
`/Users/dalley/dev/Personal/ccp/internal/cli/profile_create.go`

The success message (`"Created profile \"work\"\n  Source: ...\n  Runtime: ..."`) is useful for humans but would benefit from `--json` emitting `{"name":..., "source_dir":..., "config_dir":...}` so an agent can capture the paths without text parsing.

---

## What Is Working Well

- `profile list --json` is a clean, well-structured JSON array with `name` and `active` boolean fields. It is the model other read commands should follow.
- `profile delete --yes` correctly bypasses the confirmation prompt for non-interactive callers. The implementation in `confirm()` returns false on EOF stdin, so a closed stdin also silently declines rather than blocking -- correct behavior.
- `ccp exec <profile> -- <cmd>` is an excellent agent primitive: it sets `CLAUDE_CONFIG_DIR` and `CCP_PROFILE` for a child process without mutating global state. The child exit code is propagated via `os.Exit(ee.ExitCode())`.
- `ccp use <profile> --shell` emitting `export` lines for `eval` is the correct Unix idiom for per-shell environment changes and is directly usable from agent shell scripts.
- The `CCP_ROOT` environment variable as a filesystem sandbox for testing is excellent -- it means agents running ccp in tests can isolate state without any mocking.
- `sync pull --force` backs up before hard-resetting, which means an agent can use this flag without risking unrecoverable data loss.
- `SilenceUsage: true` and `SilenceErrors: true` on the root command prevent Cobra from printing the help banner on every error -- critical for agents parsing stderr.
- The `fslock` serializing concurrent ccp invocations means agents can safely run multiple ccp commands in parallel without profile corruption.
- The `profile doctor` command exits non-zero when errors are found, giving agents a basic health-check entry point.
- Backup timestamps use UTC in a lexicographically sortable format, making `backup.Latest()` correct without any time parsing.

---

## Score

- **2/8 read commands support --json** (profile list: yes; show, diff, doctor, current, sync status, version, sync push --dry-run: no)
- **1/4 destructive commands have a --yes / --force escape** (delete: yes; rollback, rename, sync setup with existing remote: no)
- **0/~15 errors have distinct exit codes** (all collapse to exit 1)
- **0/10 profile-name arguments have shell completion**

**Verdict: NEEDS WORK**

The agent-native foundation (exec, --yes on delete, CCP_ROOT sandbox, --shell flag) is solid. The blocking gaps are the absence of --json on diff and sync status (the two commands agents most need to branch on), the flat exit-code space (blocks reliable error handling in scripts), and the silent-empty behavior of `current`. These are all localized, low-risk additions.
