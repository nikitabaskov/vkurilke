package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReplayedNotificationCommandIsIdempotent(t *testing.T) {
	f := setup(t)
	p, err := f.s.ToggleNotificationsForUpdate(ctx, 1, 100)
	must(t, err)
	if p.Session || p.Arrival {
		t.Fatal("first command did not disable notifications")
	}
	p, err = f.s.ToggleNotificationsForUpdate(ctx, 1, 100)
	must(t, err)
	if p.Session || p.Arrival {
		t.Fatal("replayed command changed preferences twice")
	}
	p, err = f.s.ToggleNotificationsForUpdate(ctx, 1, 101)
	must(t, err)
	if !p.Session || !p.Arrival {
		t.Fatal("next command did not toggle settings")
	}
}

func TestNewerDatabaseSchemaIsNotDowngraded(t *testing.T) {
	f := setup(t)
	_, err := f.s.db.Exec(`PRAGMA user_version=2`)
	must(t, err)
	must(t, f.s.Close())
	other, err := Open(f.path, Options{})
	if err == nil {
		other.Close()
		t.Fatal("opened a newer database schema")
	}
}

func TestOpenReportsUnwritableDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.Mkdir(dir, 0500); err != nil {
		t.Fatal(err)
	}
	_, err := Open(filepath.Join(dir, "vkurilke.db"), Options{})
	if err == nil {
		t.Fatal("expected an error for a read-only data directory")
	}
	if !strings.Contains(err.Error(), "is not writable") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("error should name the directory and the cause: %v", err)
	}
}
