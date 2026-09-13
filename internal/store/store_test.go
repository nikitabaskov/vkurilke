package store

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	s     *Store
	now   time.Time
	path  string
	group Group
}

var ctx = context.Background()

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{now: time.Unix(1700000000, 0), path: filepath.Join(t.TempDir(), "test.db")}
	var err error
	f.s, err = Open(f.path, Options{AdminIDs: []int64{1}, Now: func() time.Time { return f.now }})
	must(t, err)
	t.Cleanup(func() { f.s.Close() })
	for _, u := range []User{{ID: 1, FirstName: "Никита"}, {ID: 2, FirstName: "Аня"}, {ID: 3, FirstName: "Дима"}, {ID: 4, FirstName: "Гость"}} {
		must(t, f.s.UpsertUser(ctx, u, true))
	}
	f.group, err = f.s.CreateGroup(ctx, 1, "Общага")
	must(t, err)
	for _, id := range []int64{2, 3} {
		_, err = f.s.JoinGroup(ctx, id, f.group.InviteCode)
		must(t, err)
	}
	return f
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (f *fixture) state(t *testing.T, user int64) *Status {
	t.Helper()
	s, err := f.s.Snapshot(ctx, user, f.group.ID)
	must(t, err)
	return s.Active
}
func (f *fixture) session(t *testing.T) string {
	t.Helper()
	var id string
	must(t, f.s.db.QueryRow(`SELECT id FROM sessions WHERE group_id=? AND ended_at=0`, f.group.ID).Scan(&id))
	return id
}
func (f *fixture) job(t *testing.T, kind string, user int64) Job {
	t.Helper()
	var j Job
	must(t, f.s.db.QueryRow(`SELECT id,dedupe_key,kind,group_id,session_id,user_id,actor_id,token,revision,attempts FROM outbox WHERE kind=? AND user_id=? ORDER BY id DESC LIMIT 1`, kind, user).Scan(&j.ID, &j.Key, &j.Kind, &j.GroupID, &j.SessionID, &j.UserID, &j.ActorID, &j.Token, &j.Revision, &j.Attempts))
	return j
}

func TestInviteRolesAndExclusion(t *testing.T) {
	f := setup(t)
	if _, err := f.s.CreateGroup(ctx, 2, "Чужая"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member created room: %v", err)
	}
	if _, err := f.s.Snapshot(ctx, 4, f.group.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("room leaked: %v", err)
	}
	must(t, f.s.ManageMember(ctx, 1, f.group.ID, 2, "admin"))
	if err := f.s.ManageMember(ctx, 2, f.group.ID, 1, "remove"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin removed owner: %v", err)
	}
	if err := f.s.ManageMember(ctx, 2, f.group.ID, 3, "admin"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("admin promoted peer: %v", err)
	}
	must(t, f.s.ChangeStatus(ctx, 3, f.group.ID, "smoking", 0))
	session := f.session(t)
	must(t, f.s.ManageMember(ctx, 2, f.group.ID, 3, "remove"))
	if _, err := f.s.JoinGroup(ctx, 3, f.group.InviteCode); !errors.Is(err, ErrForbidden) {
		t.Fatalf("excluded user rejoined by link: %v", err)
	}
	if err := f.s.JoinSession(ctx, 3, session); err == nil {
		t.Fatal("excluded user joined session")
	}
	must(t, f.s.ManageMember(ctx, 1, f.group.ID, 3, "add"))
	if f.state(t, 3) != nil {
		t.Fatal("readmission restored old active status")
	}
	code, err := f.s.RotateInvite(ctx, 2, f.group.ID)
	must(t, err)
	if _, err = f.s.JoinGroup(ctx, 4, f.group.InviteCode); !errors.Is(err, ErrNotFound) {
		t.Fatal("rotated invite remained valid")
	}
	_, err = f.s.JoinGroup(ctx, 4, code)
	must(t, err)
	if err = f.s.LeaveGroup(ctx, 1, f.group.ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("owner orphaned room")
	}
}
func TestOneActiveRoomAndIdempotentStatus(t *testing.T) {
	f := setup(t)
	other, err := f.s.CreateGroup(ctx, 1, "Работа")
	must(t, err)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	start := f.state(t, 1).StartedAt
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	if f.state(t, 1).StartedAt != start {
		t.Fatal("repeated status reset timer")
	}
	_, err = f.s.Snapshot(ctx, 1, other.ID)
	must(t, err)
	if f.state(t, 1).GroupID != f.group.ID {
		t.Fatal("viewing another room moved status")
	}
	oldSession := f.session(t)
	must(t, f.s.ChangeStatus(ctx, 1, other.ID, "smoking", 0))
	if active := f.state(t, 1); active.GroupID != other.ID {
		t.Fatal("did not move room")
	}
	if err = f.s.JoinSession(ctx, 2, oldSession); !errors.Is(err, ErrStale) {
		t.Fatal("old callback reopened session")
	}
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "idle", 0))
	if f.state(t, 1).GroupID != other.ID {
		t.Fatal("idle from a different room cleared active status")
	}
	var count int
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM smoker_statuses WHERE user_id=1`).Scan(&count))
	if count != 1 {
		t.Fatalf("active rows: %d", count)
	}
}
func TestSessionSurvivesFounderLeavingAndPendingArrival(t *testing.T) {
	f := setup(t)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	session := f.session(t)
	must(t, f.s.JoinSession(ctx, 2, session))
	target := f.state(t, 2).TargetTime
	f.now = f.now.Add(time.Minute)
	must(t, f.s.JoinSession(ctx, 2, session))
	if f.state(t, 2).TargetTime != target {
		t.Fatal("duplicate join reset timer")
	}
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "idle", 0))
	if f.session(t) != session {
		t.Fatal("founder departure closed session with someone going")
	}
	f.now = time.Unix(target+37, 0)
	must(t, f.s.Tick(ctx))
	active := f.state(t, 2)
	if active.Status != "smoking" || active.StartedAt != target {
		t.Fatalf("delayed arrival: %+v", active)
	}
	if f.session(t) != session {
		t.Fatal("arrival created another session")
	}
	card := f.job(t, "card", 1)
	d, err := f.s.Prepare(ctx, card)
	must(t, err)
	if d.Skip || len(d.Visits) != 2 || d.Visits[0].Seconds != 60 || d.Visits[0].Present || d.Visits[1].Name != "Аня" || !d.Visits[1].Present {
		t.Fatalf("founder card lacks visits: %+v", d)
	}
	must(t, f.s.CompleteJob(ctx, card, 500, true))
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "idle", 0))
	d, err = f.s.Prepare(ctx, f.job(t, "card", 1))
	must(t, err)
	if !d.Closed || d.Duration != 337 || len(d.Visits) != 2 || d.Visits[1].Seconds != 37 || d.Visits[1].Present {
		t.Fatalf("closed card lacks summary: %+v", d)
	}
	var separate int
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind IN ('arrival','departure')`).Scan(&separate))
	if separate != 0 {
		t.Fatal("arrivals and departures spammed separate messages")
	}
	if err = f.s.JoinSession(ctx, 3, session); !errors.Is(err, ErrStale) {
		t.Fatalf("stale callback: %v", err)
	}
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	if f.session(t) == session {
		t.Fatal("new session reused old identifier")
	}
}
func TestPresenceCheckDeliveryGraceAndRepeatedConfirmation(t *testing.T) {
	f := setup(t)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	start := f.now
	f.now = start.Add(15 * time.Minute)
	must(t, f.s.Tick(ctx))
	active := f.state(t, 1)
	if active.CheckToken == "" || active.CheckDeadline != 0 {
		t.Fatalf("bad pending reminder: %+v", active)
	}
	first := active.CheckToken
	j := f.job(t, "reminder", 1)
	f.now = f.now.Add(10 * time.Minute)
	must(t, f.s.Tick(ctx))
	if f.state(t, 1) == nil {
		t.Fatal("timed out before delivery")
	}
	d, err := f.s.Prepare(ctx, j)
	must(t, err)
	if d.Skip {
		t.Fatal("reminder skipped")
	}
	must(t, f.s.CompleteJob(ctx, j, 101, true))
	if f.state(t, 1).CheckDeadline != f.now.Add(3*time.Minute).Unix() {
		t.Fatal("grace did not start on delivery")
	}
	if err = f.s.Confirm(ctx, 2, first, true); !errors.Is(err, ErrStale) {
		t.Fatal("another user confirmed reminder")
	}
	f.now = f.now.Add(179 * time.Second)
	must(t, f.s.Confirm(ctx, 1, first, true))
	if f.state(t, 1).StartedAt != start.Unix() {
		t.Fatal("confirmation reset elapsed timer")
	}
	if err = f.s.Confirm(ctx, 1, first, true); !errors.Is(err, ErrStale) {
		t.Fatal("old confirmation remained active")
	}
	f.now = f.now.Add(15 * time.Minute)
	must(t, f.s.Tick(ctx))
	second := f.state(t, 1).CheckToken
	if second == first || second == "" {
		t.Fatal("new check did not get new token")
	}
	j = f.job(t, "reminder", 1)
	must(t, f.s.CompleteJob(ctx, j, 102, true))
	f.now = f.now.Add(3 * time.Minute)
	if err = f.s.Confirm(ctx, 1, second, true); !errors.Is(err, ErrStale) {
		t.Fatal("late reply resurrected presence")
	}
	must(t, f.s.Tick(ctx))
	if f.state(t, 1) != nil {
		t.Fatal("unanswered reminder did not turn status off")
	}
}
func TestRestartPreservesTimersAndQueue(t *testing.T) {
	f := setup(t)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	must(t, f.s.JoinSession(ctx, 2, f.session(t)))
	target := f.state(t, 2).TargetTime
	must(t, f.s.Close())
	f.now = time.Unix(target+10, 0)
	var err error
	f.s, err = Open(f.path, Options{AdminIDs: []int64{1}, Now: func() time.Time { return f.now }})
	must(t, err)
	must(t, f.s.Tick(ctx))
	if a := f.state(t, 2); a.Status != "smoking" || a.StartedAt != target {
		t.Fatalf("restart lost timer: %+v", a)
	}
	j, err := f.s.NextJob(ctx)
	must(t, err)
	if j == nil {
		t.Fatal("restart lost outbox")
	}
	var mode string
	must(t, f.s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode))
	if mode != "wal" {
		t.Fatal("WAL disabled")
	}
}
func TestOutboxRevisionAndPreferenceChecks(t *testing.T) {
	f := setup(t)
	must(t, f.s.SetPreferences(ctx, 2, Preferences{Arrival: true}))
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	var count int
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM outbox WHERE kind='card'`).Scan(&count))
	if count != 1 {
		t.Fatalf("wrong card audience: %d", count)
	}
	j := f.job(t, "card", 3)
	must(t, f.s.JoinSession(ctx, 2, f.session(t)))
	must(t, f.s.CompleteJob(ctx, j, 300, true))
	latest := f.job(t, "card", 3)
	if latest.Revision <= j.Revision {
		t.Fatal("ack lost concurrently queued card edit")
	}
	d, err := f.s.Prepare(ctx, latest)
	must(t, err)
	if d.MessageID != 300 {
		t.Fatal("retry would create another card")
	}
	must(t, f.s.SetPreferences(ctx, 1, Preferences{}))
	d, err = f.s.Prepare(ctx, f.job(t, "card", 1))
	must(t, err)
	if !d.Skip {
		t.Fatal("unsent card ignored changed preferences")
	}
	must(t, f.s.ManageMember(ctx, 1, f.group.ID, 3, "remove"))
	d, err = f.s.Prepare(ctx, latest)
	must(t, err)
	if !d.Revoked || len(d.Members) > 0 {
		t.Fatal("excluded recipient received member list")
	}
}
func TestConcurrentRoomSwitchesPreserveSingleStatus(t *testing.T) {
	f := setup(t)
	other, err := f.s.CreateGroup(ctx, 1, "Работа")
	must(t, err)
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		group := f.group.ID
		if i%2 == 0 {
			group = other.ID
		}
		wg.Add(1)
		go func() { defer wg.Done(); errs <- f.s.ChangeStatus(ctx, 1, group, "smoking", 0) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		must(t, err)
	}
	var count int
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM smoker_statuses WHERE user_id=1`).Scan(&count))
	if count != 1 {
		t.Fatal("concurrent changes duplicated active status")
	}
	must(t, f.s.db.QueryRow(`SELECT COUNT(*) FROM sessions WHERE ended_at=0`).Scan(&count))
	if count != 1 {
		t.Fatalf("orphan active sessions: %d", count)
	}
}
func TestUnreachableBotCannotLeavePresenceStuck(t *testing.T) {
	f := setup(t)
	must(t, f.s.BlockBot(ctx, 1))
	if err := f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0); !errors.Is(err, ErrBotUnavailable) {
		t.Fatal("blocked user enabled status")
	}
	must(t, f.s.UpsertUser(ctx, User{ID: 1, FirstName: "Никита"}, true))
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	must(t, f.s.BlockBot(ctx, 1))
	if f.state(t, 1) != nil {
		t.Fatal("blocked bot left active status")
	}
}
