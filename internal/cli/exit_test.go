package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dalley/ccp/internal/allowlist"
	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/profile"
	"github.com/dalley/ccp/internal/refs"
	"github.com/dalley/ccp/internal/secret"
	ccpsync "github.com/dalley/ccp/internal/sync"
)

func TestExitCodeForSentinels(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{profile.ErrNotFound, ExitUser},
		{profile.ErrAlreadyExists, ExitConflict},
		{fmt.Errorf("wrapped: %w", profile.ErrAlreadyExists), ExitConflict},
		{profile.ErrAuditSecretsDetected, ExitConflict},
		{fmt.Errorf("audit: %w", profile.ErrAuditSecretsDetected), ExitConflict},
		{secret.ErrSecretNotFound, ExitUser},
		{secret.ErrKeychainLocked, ExitState},
		{secret.ErrUnsupportedPlatform, ExitState},
		{fmt.Errorf("get: %w", secret.ErrSecretNotFound), ExitUser},
		{refs.ErrSecretRefUnresolved, ExitState},
		{fmt.Errorf("render: %w", refs.ErrSecretRefUnresolved), ExitState},
		{refs.ErrUnsupportedPlatform, ExitState},
		{allowlist.ErrMarkerNotAllowed, ExitConflict},
		{allowlist.ErrMarkerHashMismatch, ExitConflict},
		{allowlist.ErrInvalidMarker, ExitUser},
		{allowlist.ErrUnsupportedPlatform, ExitState},
		{ccpsync.ErrDirtyWorkingTree, ExitConflict},
		{fmt.Errorf("pull: %w", ccpsync.ErrRemoteUnreachable), ExitNetwork},
		{&fslock.ErrLockContended{Path: "/tmp/lock"}, ExitConflict},
		{errors.New("unstructured"), ExitUser},
	}
	for _, tc := range tests {
		got := ExitCodeFor(tc.err)
		if got != tc.want {
			t.Errorf("ExitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
}
