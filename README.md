# ccp — Claude Code Profiles

Manage multiple named Claude Code configurations on one machine. Switch between them with one command, run two in parallel with shell aliases, sync them across machines via Git.

Status: **alpha**. v1 is landing milestone by milestone.

## What it does

Claude Code reads its config from `~/.claude/` (or from `$CLAUDE_CONFIG_DIR` if set). `ccp` lets you keep several alternate config trees under `~/.config/ccp/profiles/<name>/` and point Claude at the one you want:

- `ccp use work` — set a global active profile. New shells pick it up automatically.
- `claude-work` — an optional per-profile alias that launches Claude against that profile without switching the global active. Lets you run two Claudes side-by-side.
- `ccp exec work -- claude mcp list` — one-shot, no shell state.

## Install

Pre-built binaries and Homebrew will land with M5. Until then:

```sh
go install github.com/dalley/ccp/cmd/ccp@latest
```

## Quickstart

```sh
ccp init                               # one-time setup
eval "$(ccp shell-init zsh)"           # add to ~/.zshrc
ccp profile create work --from-current # seed from your existing ~/.claude/
ccp profile create demo
ccp use work                           # set global active
ccp current                            # prints: work
```

## Relationship to jean-claude

`ccp` is a ground-up rewrite (Go, not TS) inspired by [`MikeVeerman/jean-claude`](https://github.com/MikeVeerman/jean-claude), which pioneered the `CLAUDE_CONFIG_DIR` + shared-config-overlay approach. Major differences:

| | jean-claude | ccp |
|---|---|---|
| Switching model | Parallel aliases only | Single-active + optional aliases |
| Language | TypeScript (Node ≥18) | Go (single binary) |
| Auto-activation | No | Planned (v2, direnv-style allow-list) |
| Secrets separation | No | Planned (v2) |
| `sync pull` | Destructive (`reset --hard`) | Non-destructive (stash/pop) |
| Path portability | Absolute paths | `$HOME`/`~` everywhere |
| Windows | Unsupported | Planned (v2.1) |

## License

MIT
