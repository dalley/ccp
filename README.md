# ccp — Claude Code Profiles

**Profile manager for Claude Code.** Switch between work and personal Claude Code accounts with one command, run two profiles side by side, sync your configs across machines via Git.

`ccp` is like `direnv` or `pyenv`, but for `CLAUDE_CONFIG_DIR`. It manages multiple named Claude Code profiles on one machine — settings, agents, commands, hooks, skills, output-styles, keybindings, and `CLAUDE.md` — and lets you swap between them without editing files by hand. Your default `~/.claude/` is never touched; removing the shell-init line reverses everything.

Status: **alpha** (v1 feature-complete, pre-release). A v2 branch in review adds auto-activation via a `.claude-profile` marker (direnv-style allow-list) and secrets separation with OS keychain + `op://` resolution — see the Roadmap section.

## What it does

Claude Code reads its config from `~/.claude/` (or from `$CLAUDE_CONFIG_DIR` if set). `ccp` lets you keep several alternate config trees under `~/.config/ccp/profiles/<name>/` and point Claude at the one you want:

- `ccp use work` — set a global active profile. New shells pick it up automatically.
- `claude-work` — an optional per-profile alias that launches Claude against that profile without switching the global active. Lets you run two Claudes side-by-side.
- `ccp exec work -- claude mcp list` — one-shot, no shell state.

## Use cases

- **Work vs. personal Claude Code accounts** on the same laptop — different auth, different MCP servers, different `CLAUDE.md` memory.
- **Run two Claude Code instances in parallel** in separate terminals (`claude-work` in one, `claude-personal` in another) without their sessions clobbering each other.
- **Sync your Claude Code setup across machines** — keep your agents, slash commands, hooks, skills, and `CLAUDE.md` in Git; clone on a new laptop and every profile is there.
- **Try experimental profiles** (new MCP servers, draft agents, evaluation setups) without touching your daily-driver config.
- **Team-ish workflows** — share a read-only "team" profile via Git alongside your personal one.

## Install

### Homebrew (macOS, Linux) — once the tap is published

```sh
brew install dalley/ccp/ccp
```

### GitHub Releases — pre-built binaries

Download the archive for your platform from the [releases page](https://github.com/dalley/ccp/releases), extract, and drop `ccp` on your `PATH`.

### npm

```sh
npm install -g @dalley/ccp
```

The npm package downloads the platform binary in a `postinstall` hook; Node is not required at runtime.

### From source

```sh
go install github.com/dalley/ccp/cmd/ccp@latest
```

## Quickstart

```sh
ccp init                                 # one-time setup
eval "$(ccp shell-init zsh)"             # add this line to ~/.zshrc
ccp profile create work --from-current   # seed from your existing ~/.claude/
ccp profile create demo --from work      # clone work into demo
ccp use work                             # set global active
ccp current                              # → work

# Switch between profiles
ccp use demo

# Run against a profile without switching
ccp exec work -- claude --help

# See what differs between two profiles
ccp profile diff work demo

# Health check
ccp profile doctor

# Recover from a bad delete
ccp profile rollback
```

### Multi-machine sync

```sh
# Machine A — create a repo (e.g. on GitHub/GitLab) first, then:
ccp sync setup --url git@github.com:you/my-claude-profiles.git
ccp sync push

# Machine B
ccp sync setup --url git@github.com:you/my-claude-profiles.git
ccp profile refresh        # rebuild runtime symlinks from the cloned source
```

Profiles use `$HOME`-relative paths, so they work without edit on a machine with a different username.

### Prompt integration

```sh
# zsh / bash
PS1='[$(ccp prompt --prefix "ccp:")] %~ $ '

# starship.toml
format = '$(ccp prompt --prefix "ccp:")\n$character'
```

## Commands

```
ccp init                              one-time setup
ccp shell-init {zsh,bash,fish}        shell snippet for activation
ccp profile list [--json]             list profiles
ccp profile show [name]               profile details
ccp profile create <name> [--from-current|--from <other>] [--alias]
ccp profile use <name> [--shell]      set active (or emit export for current shell)
ccp profile delete <name> [--yes]     move source + runtime to a backup
ccp profile rename <old> <new>
ccp profile diff <a> [b]              recursive content diff (defaults b=active)
ccp profile doctor [name]             validate symlinks / schema / alias blocks
ccp profile refresh [name]            rebuild runtime symlinks from source
ccp profile rollback                  restore the most recent backup
ccp current                           print active profile name
ccp use <name> [--shell]              shortcut for `ccp profile use`
ccp exec <name> -- <cmd...>           run <cmd> with CLAUDE_CONFIG_DIR set
ccp sync setup [--url <git>]
ccp sync push [--dry-run] [--quiet]   --quiet suppresses the pre-push audit advisory
ccp sync pull [--force]               non-destructive by default
ccp sync status
ccp prompt [--prefix X] [--suffix Y]  print active profile (empty if none)
ccp completion {zsh,bash,fish}        completion script
ccp version
ccp secret set <profile> <key>           store a value in the OS keychain (file fallback)
ccp secret get <profile> <key>           read a stored value
ccp secret list <profile> [--json]       list stored keys
ccp secret rm <profile> <key>            remove a stored value
ccp allow [--status] [--json]            approve the current dir's .claude-profile
ccp deny                                 revoke current dir's .claude-profile approval
ccp profile audit <name> [--json]        detect suspected secrets in a profile
ccp profile export <name> [-o path]      export a portable tarball (strips secrets by default)
```

## Exit codes

Every `ccp` command returns one of these codes so agents can branch without parsing stderr:

| Code | Meaning | Examples |
|---|---|---|
| `0` | success | any command that completed its intent |
| `1` | user error | invalid profile name, missing required argument, unknown subcommand |
| `2` | state or IO error | manifest unreadable, disk full (`ENOSPC`), read-only filesystem (`EROFS`), permission denied |
| `3` | network error | unreachable remote, DNS failure, HTTPS TLS issues on git push/pull/clone/fetch |
| `4` | conflict | `ErrAlreadyExists` on create, dirty tree on `sync pull`, lock held by another ccp process |

The mapping lives in [`internal/cli/exit.go`](internal/cli/exit.go). Agents can rely on these codes staying stable across patch releases within a minor version.

## How switching works

A ccp "profile" is a directory under `~/.config/ccp/profiles/<name>/` holding the portable subset of Claude's config (settings.json, agents/, commands/, hooks/, skills/, output-styles/, keybindings.json, CLAUDE.md). When you activate a profile, ccp sets `CLAUDE_CONFIG_DIR=$HOME/.claude-<name>`, a runtime directory of symlinks back into the source. Claude writes session/auth/cache files there; the symlinks keep shareable config in sync.

Activation is resolved in this order:

1. `CLAUDE_CONFIG_DIR` already in env (escape hatch)
2. `CCP_PROFILE` env var
3. `~/.config/ccp/manifest.toml:active_profile` (set by `ccp use`)

Your default `~/.claude/` is never touched. Remove the `shell-init` line from your shellrc to reverse everything.

## Secrets and references (v2 preview)

Profile files can contain `{{ keychain:KEY }}`, `{{ op://vault/item/field }}`, and `{{ env.VAR }}` references that ccp resolves at activation time into real files in the runtime directory (`~/.claude-<name>/`). Only resolved, real-valued files ever reach Claude Code; the source tree in `profiles/<name>/` keeps the ref text. Keychain values are stored via `ccp secret set` — the OS keychain (macOS Keychain, libsecret on Linux) is used where available, with a file fallback to `~/.config/ccp/secrets/<profile>.json` (mode 0600, gitignored, per-profile). `op://` refs shell out to the 1Password CLI.

### Quickstart

```sh
ccp secret set work ANTHROPIC_API_KEY sk-xxx
# Then in ~/.config/ccp/profiles/work/settings.json:
# "apiKey": "{{ keychain:ANTHROPIC_API_KEY }}"
ccp use work   # settings.json is now rendered with the resolved value
```

### Auditing existing profiles

```sh
ccp profile audit work
```
Scans for suspected secrets (AWS keys, GitHub tokens, Stripe keys, JWTs, PEM private keys, high-entropy strings) so you can migrate them to keychain storage.

### Escape sequence for literal refs

`{{!}}` preceding a ref makes it literal — use this when documenting the syntax inside a profile file that ccp will render.

## Auto-activation (v2 preview)

Drop a `.claude-profile` file in a project root containing a single-line profile name:

```
work
```

Approve it once: `ccp allow`. After that, `cd`ing into the project (or any subdir) automatically sets `CLAUDE_CONFIG_DIR` for new and existing shells via the `shell-init` hook. Safety model is direnv-style fail-closed: unapproved or modified markers warn once and do nothing.

### Escape hatches

- `CCP_PROFILE_AUTO=0` — disable the auto-activation layer entirely
- Setting `CLAUDE_CONFIG_DIR` or `CCP_PROFILE` manually always wins
- `ccp allow --status` — see the current state of the marker at `$PWD`

## Relationship to jean-claude

`ccp` is a ground-up rewrite in Go inspired by [`MikeVeerman/jean-claude`](https://github.com/MikeVeerman/jean-claude), which pioneered the `CLAUDE_CONFIG_DIR` + shared-config-overlay approach.

| | jean-claude | ccp |
|---|---|---|
| Switching model | Parallel aliases only | Single active + optional parallel aliases |
| Language | TypeScript (Node ≥18) | Go (single static binary) |
| Path portability | Absolute paths in `profiles.json` and aliases | `$HOME`/`~` everywhere |
| `sync pull` | Destructive (`reset --hard` by default) | Non-destructive (refuses on dirty tree) |
| Doctor / rollback | — | ✓ |
| Content-level directory diff | Directory-existence only | Per-file SHA-256 |
| Completions | — | bash, zsh, fish |
| Secrets separation | — | ✓ (v2, keychain + `op://` + file fallback) |
| Auto-activation | — | ✓ (v2, direnv-style allow-list) |
| Windows | Unsupported | Planned (v2.1) |

## Roadmap

- **v2.0** — Shipping: auto-activation via `.claude-profile` marker + content-hash allow-list, secrets separation with OS keychain + `op://` reference resolution.
- **v2.1** — Windows (PowerShell integration, copy mode in place of POSIX symlinks).
- **v3.x** — Profile plugins, team profile import from Git URL with safe update flow.

## Gotchas

- **Headless Linux and devcontainers**: no OS keychain backend is available by default. `ccp secret set` will write to `~/.config/ccp/secrets/<profile>.json` (0600, gitignored) and print a one-time warning. Install `libsecret` / `gnome-keyring` for keychain-backed storage.
- **1Password CLI prompting**: `op://` refs may trigger biometric unlock on first use. In non-TTY contexts (CI, shell hooks), set `OP_SERVICE_ACCOUNT_TOKEN` or the ref will be refused with a clear error.
- **Allow-list is per-machine**: `~/.config/ccp/allowlist.toml` is gitignored and deliberately not synced across machines. You must `ccp allow` once on each workstation. This is a deliberate tradeoff — see "Deviations from linked issues" below.
- **Content-only marker hash**: the allow-list hashes only the contents of `.claude-profile`, not the path. Moving or renaming a file with identical contents does NOT require re-approval; editing its content DOES. This preserves sync-across-machines at a small supply-chain cost documented in the plan.
- **Cross-directory allow-list staleness**: the allow-list keys on marker path, so an approval survives deleting the project at that path. If you later clone a different (potentially hostile) repo into the same directory and its `.claude-profile` happens to contain the same profile name with matching content, auto-activation honours the old approval without prompting. Mitigation: run `ccp deny` before deleting or replacing any directory whose marker you previously approved. `ccp allow --status` also surfaces this — the approval lingers until you explicitly revoke it.
- **Shell hook latency**: cache hit is <5ms (pure shell); cache miss forks `ccp` and typically takes 30-80ms cold. `CCP_PROFILE_AUTO=0` turns the whole layer off.
- **`{{` collision**: ccp only treats `{{` as a ref when it matches one of three schemes (`keychain:`, `op://`, `env.`). Other `{{ ... }}` content (Helm, Handlebars, prose) passes through untouched. Use `{{!}}` to force literal output.
- **Executable bit preservation**: rendered files preserve the user-execute bit from the source — so a ref-bearing 0755 hook script lands as 0700 in the runtime dir, not 0600.

## Deviations from linked issues (#4, #5)

During implementation, a few design decisions diverged from the original issues:

- **Allow-list is `allowlist.toml` not `allowed.json`** — matches existing ccp manifest conventions.
- **Hash is content-only, not path+content** — direnv's path-in-hash breaks ccp's sync-across-machines tagline. Threat model: the marker content is just a validated profile name, and the attack surface (the profile's hooks) requires the profile to already exist locally.
- **Reference syntax is profile-implicit**: `{{ keychain:KEY }}` (not `{{ keychain://ccp/<profile>/<key> }}`) — refs transport cleanly across profile renames and copies.
- **Export audit is advisory by default**; `--fail-on-audit` opts into refusing export on findings.
- **`ccp exec` refreshes symlinks only when the profile contains refs** (`refs.HasAnyRefs` short-circuit); `--no-refresh` flag skips even when refs exist.
- **Windows commands are unregistered**, not runtime-errored — the commands simply don't appear in `ccp --help` under Windows. Windows backend work is tracked in issue #6.

## License

MIT
