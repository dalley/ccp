//go:build windows

package allowlist

// Windows support lands with issue #6. All operations on Windows return
// ErrUnsupportedPlatform so the CLI can exit cleanly with ExitState rather
// than panicking in syscall-land. The commands that use this package
// (`ccp allow`, `ccp deny`, `ccp shell-resolve-dir`) are hidden on Windows
// at the CLI layer, so users should never see these errors directly —
// they exist as defense-in-depth for internal callers.

// Status mirrors the POSIX shape so packages importing us compile on
// Windows without build-tag acrobatics.
type Status int

const (
	StatusUnallowed Status = iota
	StatusAllowed
	StatusHashMismatch
)

func (s Status) String() string {
	switch s {
	case StatusUnallowed:
		return "Unallowed"
	case StatusAllowed:
		return "Allowed"
	case StatusHashMismatch:
		return "HashMismatch"
	default:
		return "Status(?)"
	}
}

// File mirrors the POSIX shape for compile-compatibility.
type File struct {
	SchemaVersion int
	Entries       map[string]string
}

// CurrentSchemaVersion is kept in sync with the POSIX build.
const CurrentSchemaVersion = 1

func Default() File { return File{SchemaVersion: CurrentSchemaVersion} }

func Hash(string) (string, error) {
	return "", ErrUnsupportedPlatform
}

func ReadName(string) (string, error) {
	return "", ErrUnsupportedPlatform
}

func FindMarker(string, string) (string, error) {
	return "", ErrUnsupportedPlatform
}

func Load(string) (File, bool, error) {
	return File{}, false, ErrUnsupportedPlatform
}

func Save(string, File) error {
	return ErrUnsupportedPlatform
}

func Approve(string, string) error {
	return ErrUnsupportedPlatform
}

func Revoke(string, string) error {
	return ErrUnsupportedPlatform
}

func Check(string, string) (Status, string, error) {
	return StatusUnallowed, "", ErrUnsupportedPlatform
}
