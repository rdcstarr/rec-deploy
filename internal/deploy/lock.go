package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lock takes an exclusive advisory lock on path, serialising deploys to one
// working tree. Two pushes in quick succession would otherwise run two
// concurrent `git reset --hard` on the same checkout — an old implementation has no
// lock at all.
//
// It polls with LOCK_NB rather than blocking inside flock(2), because a blocking
// flock cannot be interrupted by a cancelled context, and Ctrl+C must always
// cancel a deploy.
func Lock(ctx context.Context, dir, path string) (func(), error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create locks dir: %w", err)
	}

	// The lock file is named for the hash of the path: a path is not a filename,
	// and the hash is fixed-length, collision-free enough and needs no escaping.
	sum := sha256.Sum256([]byte(path))
	lockFile := filepath.Join(dir, hex.EncodeToString(sum[:])+".lock")

	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock for %s: %w", path, err)
	}

	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()

	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}

		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, fmt.Errorf("waiting for the deploy lock on %s: %w — another deploy holds it", path, ctx.Err())
		case <-tick.C:
		}
	}
}
