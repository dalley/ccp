package fsutil

import (
	"path/filepath"
	"testing"
)

func TestSymlinkWithin(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name       string
		linkPath   string
		linkTarget string
		want       bool
	}{
		{"relative sibling stays within", filepath.Join(root, "a"), "b", true},
		{"relative dotdot escapes", filepath.Join(root, "a"), "../outside", false},
		{"absolute target inside root", filepath.Join(root, "a"), filepath.Join(root, "b"), true},
		{"absolute target outside root", filepath.Join(root, "a"), "/etc/hosts", false},
		{"self-reference to root is rejected", filepath.Join(root, "a"), ".", false},
		{"empty target is rejected", filepath.Join(root, "a"), "", false},
		{"target is root itself (absolute)", filepath.Join(root, "a"), root, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SymlinkWithin(tc.linkPath, tc.linkTarget, root)
			if got != tc.want {
				t.Errorf("SymlinkWithin(%q, %q, %q) = %v, want %v",
					tc.linkPath, tc.linkTarget, root, got, tc.want)
			}
		})
	}
}
