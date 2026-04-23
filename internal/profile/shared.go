package profile

// SharedItem is one file or directory that makes up the portable surface of a
// Claude Code configuration — the bits a user would want to share across
// machines or between profiles. Items not listed here are left untouched in
// Claude's runtime config directory (session files, caches, auth tokens,
// conversation history, etc.).
type SharedItem struct {
	// Name is the basename under ~/.claude/ and under the profile source dir.
	Name string
	// Dir is true when the item is a directory; false for a regular file.
	Dir bool
}

// SharedItems is the canonical list of profile-swappable artifacts.
//
// Derived from jean-claude's SHARED_ITEMS (src/lib/profiles.ts) with
// adjustments:
//   - plugins/ omitted (it's primarily a 10+MB cache of downloaded plugin
//     bundles, not user-authored config — profile-specific plugin state
//     belongs in settings.json:enabledPlugins, which IS shared).
//   - commands/ and output-styles/ added.
//   - CLAUDE.md shared by default (user memory is high-value and text-only).
var SharedItems = []SharedItem{
	{Name: "settings.json", Dir: false},
	{Name: "keybindings.json", Dir: false},
	{Name: "CLAUDE.md", Dir: false},
	{Name: "agents", Dir: true},
	{Name: "commands", Dir: true},
	{Name: "hooks", Dir: true},
	{Name: "output-styles", Dir: true},
	{Name: "skills", Dir: true},
}
