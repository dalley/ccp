package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/dalley/ccp/internal/fslock"
	"github.com/dalley/ccp/internal/profile"
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
