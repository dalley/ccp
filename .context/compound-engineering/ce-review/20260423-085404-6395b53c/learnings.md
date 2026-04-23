# Institutional Learnings Search Results

## Search Context

- **Feature/Task**: Go CLI project (`ccp`) — Claude config profile manager using symlinks, go-git, Cobra, TOML manifests, goreleaser, shell integration (zsh/bash/fish), npm distribution
- **Keywords Used**: symlink, go-git, golang, cobra, goreleaser, homebrew, brew tap, flock, advisory lock, atomic write, TOML, shell-init, chpwd, PROMPT_COMMAND, direnv, zsh, fish, npm postinstall, binary distribution, keychain, cross-platform, cross-machine, $HOME, path portability
- **Directories Searched**: `/Users/dalley/.claude/plugins/marketplaces/compound-engineering-plugin/docs/solutions/`, `/Users/dalley/dev/` (glob `**/docs/solutions/*.md`)
- **Files Scanned**: 23 total solution files
- **Relevant Matches**: 0 directly relevant; 2 tangentially relevant (noted below)

## Critical Patterns

No `critical-patterns.md` file exists in the searched solutions directories. No standing critical patterns apply.

## Relevant Learnings

### No Direct Matches Found

None of the 23 documented solutions cover:

- Symlink management (creation, dangling link detection, atomic swap via `os.Rename`)
- go-git/v5 usage patterns or pitfalls
- Cobra CLI command structure patterns
- goreleaser + Homebrew tap configuration
- Cross-machine `$HOME`-anchored path portability
- `flock` / advisory locking in Go
- Shell integration hooks (`chpwd`, `PROMPT_COMMAND`, fish `--on-variable PWD`)
- npm `postinstall` binary distribution (fetch platform binary from GitHub Releases)
- OS keychain integration (macOS Keychain, libsecret)
- TOML atomic write (write-to-temp + `os.Rename`)

The existing solutions corpus is scoped entirely to the compound-engineering plugin ecosystem (TypeScript CLI, skill/converter architecture, plugin release workflows) and has no overlap with systems-level Go CLI development.

## Tangentially Relevant Learnings

### 1. Colon-Namespaced Names Break Filesystem Paths on Windows
- **File**: `/Users/dalley/.claude/plugins/marketplaces/compound-engineering-plugin/docs/solutions/integrations/colon-namespaced-names-break-windows-paths-2026-03-26.md`
- **Problem Type**: integration_issue / path-sanitization
- **Relevance**: `ccp` uses `~/.claude-<name>/` directory naming and `CLAUDE_CONFIG_DIR`. If profile names ever contain characters illegal on Windows (colons, pipes, etc.), the same class of bug would surface. Low risk today (macOS/Linux target), but worth noting if Windows support is planned.
- **Key Insight**: Never use user-supplied identifiers directly in `path.Join()` calls without sanitizing for filesystem-illegal characters on all target platforms. Replacing colons with hyphens (not slashes) is the safe choice.
- **Severity**: high (was a blocker on Windows for that project)

### 2. Agent-Friendly CLI Principles
- **File**: `/Users/dalley/.claude/plugins/marketplaces/compound-engineering-plugin/docs/solutions/agent-friendly-cli-principles.md`
- **Problem Type**: best_practice
- **Relevance**: `ccp shell-init` and profile-switching commands will be invoked by both humans and potentially agents/scripts. The rubric covers non-interactive execution (`--no-input`), structured output, idempotent mutating commands, and clean stderr/stdout separation — all applicable to a config-management CLI.
- **Key Insight**: Mutating commands (activate, sync, create) should be idempotent where feasible and must not block on interactive prompts. Provide `--no-input` / `--quiet` escape hatches from the start.
- **Severity**: medium

## Recommendations

Based on the search, there are no prior-art pitfalls documented in this knowledge base for the specific technologies `ccp` uses. The following are inferred from the tangential matches above:

1. **Profile name sanitization**: Validate and sanitize profile names at creation time (reject or escape characters that are invalid in directory names). This prevents the colon-path class of bug documented above.

2. **Idempotent activation**: The `ccp activate <name>` / `ccp deactivate` flow should be idempotent — running it twice should not error or leave a broken symlink state. Document this invariant explicitly.

3. **Non-interactive defaults**: `ccp shell-init` emits shell snippets to stdout for eval. Ensure all other commands (especially `sync`) are safe to call from non-interactive contexts (CI, cron, agent invocations) without blocking on prompts.

4. **No prior learnings on the core technical risks** (go-git pitfalls, flock behavior on macOS/NFS, goreleaser tap config, npm postinstall binary fetch, atomic rename semantics) exist in this knowledge base. These areas carry implementation risk that must be sourced from external references or first-principles reasoning during review.
