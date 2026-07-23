package deploy

import (
	"context"
	"testing"
	"time"
)

// Two pushes in quick succession must not run two concurrent `git reset --hard`
// on the same working tree. an old implementation has no lock at all.
func TestLockIsExclusivePerPath(t *testing.T) {
	dir := t.TempDir()

	release, err := Lock(context.Background(), dir, "/var/www/api")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	if _, err := Lock(ctx, dir, "/var/www/api"); err == nil {
		t.Fatal("a second Lock on the same path succeeded while the first was held")
	}

	release()

	// Once released, the path is lockable again.
	release2, err := Lock(context.Background(), dir, "/var/www/api")
	if err != nil {
		t.Fatalf("Lock after release: %v", err)
	}
	release2()
}

func TestLockDoesNotBlockOtherPaths(t *testing.T) {
	dir := t.TempDir()

	releaseA, err := Lock(context.Background(), dir, "/var/www/a")
	if err != nil {
		t.Fatalf("Lock a: %v", err)
	}
	defer releaseA()

	releaseB, err := Lock(context.Background(), dir, "/var/www/b")
	if err != nil {
		t.Fatalf("Lock b: %v — different paths must not contend", err)
	}
	releaseB()
}
