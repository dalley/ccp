# ccp — Claude Code Profiles

Manage multiple named Claude Code configurations on one machine. Switch between them with one command, run two in parallel with shell aliases, sync them across machines via Git.

Status: **alpha** (v1 feature-complete, pre-release).

## What it does

Claude Code reads its config from `~/.claude/` (or from `$CLAUDE_CONFIG_DIR` if set). `ccp` lets you keep several alternate config trees under `~/.config/ccp/profiles/<name>/` and point Claude at the one you want:

- `ccp use work` — set a global active profile. New shells pick it up automatically.
- `claude-work` — an optional per-profile alias that launches Claude against that profile without switching the global active. Lets you run two Claudes side-by-side.
- `ccp exec work -- claude mcp list` — one-shot, no shell state.

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
ccp sync push [--dry-run]
ccp sync pull [--force]               non-destructive by default
ccp sync status
ccp prompt [--prefix X] [--suffix Y]  print active profile (empty if none)
ccp completion {zsh,bash,fish}        completion script
ccp version
```

## How switching works

A ccp "profile" is a directory under `~/.config/ccp/profiles/<name>/` holding the portable subset of Claude's config (settings.json, agents/, commands/, hooks/, skills/, output-styles/, keybindings.json, CLAUDE.md). When you activate a profile, ccp sets `CLAUDE_CONFIG_DIR=$HOME/.claude-<name>`, a runtime directory of symlinks back into the source. Claude writes session/auth/cache files there; the symlinks keep shareable config in sync.

Activation is resolved in this order:

1. `CLAUDE_CONFIG_DIR` already in env (escape hatch)
2. `CCP_PROFILE` env var
3. `~/.config/ccp/manifest.toml:active_profile` (set by `ccp use`)

Your default `~/.claude/` is never touched. Remove the `shell-init` line from your shellrc to reverse everything.

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
| Secrets separation | — | Planned (v2, keychain + `op://`) |
| Auto-activation | — | Planned (v2, direnv-style allow-list) |
| Windows | Unsupported | Planned (v2.1) |

## Roadmap

- **v2.0** — Auto-activation via `.claude-profile` marker and direnv-style content-hash allow-list. Secrets separation (`secrets/<name>.json`, keychain + `op://` reference resolution).
- **v2.1** — Windows (PowerShell integration, copy mode in place of POSIX symlinks).
- **v3.x** — Profile plugins, team profile import from Git URL with safe update flow.

## License

MIT
