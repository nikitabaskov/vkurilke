package store

import "testing"

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
