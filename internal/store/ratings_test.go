package store

import (
	"errors"
	"testing"
	"time"
)

func TestSessionRatingsPersistenceAuthorizationAndIsolatedRefresh(t *testing.T) {
	f := setup(t)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	session := f.session(t)
	for _, user := range []int64{1, 2, 3} {
		must(t, f.s.CompleteJob(ctx, f.job(t, "card", user), 100+user, true))
	}
	if err := f.s.RateSession(ctx, 1, session, 1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("rated active session: %v", err)
	}
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "smoking", 0))
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "idle", 0))
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "idle", 0))
	for _, user := range []int64{1, 2, 3} {
		must(t, f.s.CompleteJob(ctx, f.job(t, "card", user), 100+user, true))
	}
	must(t, f.s.Tick(ctx)) // Draining delivery must not delete history.
	if err := f.s.RateSession(ctx, 3, session, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("nonparticipant rated: %v", err)
	}
	if err := f.s.RateSession(ctx, 4, session, 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider rated: %v", err)
	}
	if err := f.s.RateSession(ctx, 1, session, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid rating: %v", err)
	}
	if err := f.s.RateSession(ctx, 1, "missing", 1); !errors.Is(err, ErrStale) {
		t.Fatalf("missing session rating: %v", err)
	}
	must(t, f.s.RateSession(ctx, 1, session, 1))
	job := f.job(t, "card", 1)
	must(t, f.s.RateSession(ctx, 1, session, 1))
	if f.job(t, "card", 1).Revision != job.Revision {
		t.Fatal("duplicate vote queued another edit")
	}
	must(t, f.s.RateSession(ctx, 1, session, -1))
	must(t, f.s.RateSession(ctx, 2, session, 1))
	d, err := f.s.Prepare(ctx, f.job(t, "card", 1))
	must(t, err)
	if d.Likes != 1 || d.Dislikes != 1 || d.Rating != -1 || !d.CanRate || !d.Closed || d.StartedAt == 0 || d.EndedAt-d.StartedAt != 60 {
		t.Fatalf("rating summary: %+v", d)
	}
	d, err = f.s.Prepare(ctx, f.job(t, "card", 3))
	must(t, err)
	if d.CanRate || d.Rating != 0 || d.Likes != 1 {
		t.Fatalf("spectator rating state: %+v", d)
	}
	for _, user := range []int64{1, 2, 3} {
		must(t, f.s.CompleteJob(ctx, f.job(t, "card", user), 100+user, true))
	}
	must(t, f.s.Close())
	f.s, err = Open(f.path, Options{AdminIDs: []int64{1}, Now: func() time.Time { return f.now }})
	must(t, err)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	newSession := f.session(t)
	var count int
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE session_id=?`, session).Scan(&count))
	if count != 0 {
		t.Fatal("new session refreshed archived cards")
	}
	newJob := f.job(t, "card", 1)
	must(t, f.s.RateSession(ctx, 1, session, 1))
	var revision int64
	must(t, f.s.db.QueryRow(`SELECT revision FROM outbox WHERE session_id=? AND user_id=1`, newSession).Scan(&revision))
	if revision != newJob.Revision {
		t.Fatal("old rating touched new session")
	}
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM session_ratings WHERE session_id=?`, session).Scan(&count))
	if count != 2 {
		t.Fatal("votes lost across restart or duplicated")
	}
	must(t, f.s.ManageMember(ctx, 1, f.group.ID, 2, "remove"))
	if err = f.s.RateSession(ctx, 2, session, -1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("removed member rated: %v", err)
	}
}

func TestSchemaUpgradePreservesSessionHistory(t *testing.T) {
	f := setup(t)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	session := f.session(t)
	_, err := f.s.db.Exec(`DROP TABLE session_ratings; PRAGMA user_version=1`)
	must(t, err)
	must(t, f.s.Close())
	f.s, err = Open(f.path, Options{Now: func() time.Time { return f.now }})
	must(t, err)
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "idle", 0))
	must(t, f.s.RateSession(ctx, 1, session, 1))
	stats, err := f.s.Statistics(ctx, 1, "", "all")
	must(t, err)
	if stats.Totals != (SmokingTotals{Outings: 1, Seconds: 60}) {
		t.Fatalf("upgrade lost history: %+v", stats)
	}
	var version int
	must(t, f.s.db.QueryRow(`PRAGMA user_version`).Scan(&version))
	if version != 2 {
		t.Fatalf("schema version: %d", version)
	}
}
