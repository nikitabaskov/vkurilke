package store

import (
	"errors"
	"testing"
	"time"
)

func TestStatisticsPeriodsAndClippedVisits(t *testing.T) {
	f := setup(t)
	f.now = time.Date(2026, 9, 13, 12, 0, 0, 0, ReportLocation)
	// Each visit crosses one boundary. Count it only where it started, but
	// include the part of its duration inside every reporting window.
	boundaries := []time.Time{
		time.Date(2026, 9, 13, 6, 0, 0, 0, ReportLocation),
		f.now.Add(-7 * 24 * time.Hour),
		f.now.Add(-30 * 24 * time.Hour),
	}
	for _, boundary := range boundaries {
		id := randomID()
		start, end := boundary.Add(-time.Minute).Unix(), boundary.Add(time.Minute).Unix()
		_, err := f.s.db.Exec(`INSERT INTO sessions(id,group_id,founder_id,started_at,ended_at) VALUES(?,?,1,?,?)`, id, f.group.ID, start, end)
		must(t, err)
		_, err = f.s.db.Exec(`INSERT INTO session_visits(episode_id,session_id,user_id,started_at,ended_at) VALUES(?,?,1,?,?)`, randomID(), id, start, end)
		must(t, err)
	}
	for _, tc := range []struct {
		period           string
		outings, seconds int64
	}{
		{"today", 0, 60}, {"week", 1, 180}, {"month", 2, 300}, {"all", 3, 360},
	} {
		t.Run(tc.period, func(t *testing.T) {
			stats, err := f.s.Statistics(ctx, 1, f.group.ID, tc.period)
			must(t, err)
			if stats.Timezone != "Asia/Novosibirsk" {
				t.Fatalf("wrong statistics timezone: %q", stats.Timezone)
			}
			if stats.Totals != (SmokingTotals{Outings: tc.outings, Seconds: tc.seconds}) || stats.Sessions != tc.outings || stats.SessionSeconds != tc.seconds {
				t.Fatalf("wrong clipped statistics: %+v", stats)
			}
			if len(stats.Leaderboard) != 3 || stats.Leaderboard[0].UserID != 1 || stats.Leaderboard[0].SmokingTotals != stats.Totals {
				t.Fatalf("wrong leaderboard: %+v", stats.Leaderboard)
			}
		})
	}
	for _, hour := range []int{0, 3, 5, 6, 23} {
		now := time.Date(2026, 1, 1, hour, 0, 0, 0, ReportLocation)
		want := time.Date(2026, 1, 1, 6, 0, 0, 0, ReportLocation)
		if hour < 6 {
			want = want.AddDate(0, 0, -1)
		}
		got, err := statisticsStart(now.UTC(), "today")
		must(t, err)
		if got != want.Unix() {
			t.Fatalf("day boundary at %v: got %v, want %v", now, time.Unix(got, 0), want)
		}
	}
	if _, err := f.s.Statistics(ctx, 1, "", "year"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid period accepted: %v", err)
	}
}

func TestStatisticsRepeatedArrivalsRoomSwitchAndAccess(t *testing.T) {
	f := setup(t)
	other, err := f.s.CreateGroup(ctx, 1, "Работа")
	must(t, err)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "going", 5))
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "idle", 0))
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0)) // retry is not an outing
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "smoking", 0))
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, other.ID, "smoking", 0))
	f.now = f.now.Add(time.Minute)
	personal, err := f.s.Statistics(ctx, 1, "", "all")
	must(t, err)
	if personal.Totals != (SmokingTotals{Outings: 3, Seconds: 240}) {
		t.Fatalf("personal: %+v", personal)
	}
	stats, err := f.s.Statistics(ctx, 2, f.group.ID, "all")
	must(t, err)
	if stats.Totals != (SmokingTotals{Outings: 3, Seconds: 300}) || stats.Sessions != 1 || stats.SessionSeconds != 300 {
		t.Fatalf("room: %+v", stats)
	}
	if stats.Leaderboard[0].UserID != 1 || stats.Leaderboard[0].Outings != 2 || stats.Leaderboard[0].Seconds != 180 || stats.Leaderboard[1].Seconds != 120 {
		t.Fatalf("leaderboard: %+v", stats.Leaderboard)
	}
	if _, err := f.s.Statistics(ctx, 4, f.group.ID, "all"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("outsider saw room: %v", err)
	}
	must(t, f.s.ManageMember(ctx, 1, f.group.ID, 2, "remove"))
	if _, err := f.s.Statistics(ctx, 2, f.group.ID, "all"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("banned member saw room: %v", err)
	}
	stats, err = f.s.Statistics(ctx, 1, f.group.ID, "all")
	must(t, err)
	if len(stats.Leaderboard) != 2 || stats.Totals.Seconds != 300 {
		t.Fatalf("lost former member totals: %+v", stats)
	}
	personal, err = f.s.Statistics(ctx, 2, "", "all")
	must(t, err)
	if personal.Totals != (SmokingTotals{Outings: 1, Seconds: 120}) {
		t.Fatalf("lost own history after removal: %+v", personal)
	}
}

func TestStatisticsArrivalTimerAndDeterministicTies(t *testing.T) {
	f := setup(t)
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "going", 5))
	stats, err := f.s.Statistics(ctx, 2, "", "all")
	must(t, err)
	if stats.Totals != (SmokingTotals{}) {
		t.Fatalf("counted going: %+v", stats)
	}
	f.now = f.now.Add(6 * time.Minute)
	stats, err = f.s.Statistics(ctx, 2, "", "all")
	must(t, err)
	if stats.Totals != (SmokingTotals{Outings: 1, Seconds: 60}) {
		t.Fatalf("arrival timer: %+v", stats)
	}
	must(t, f.s.ChangeStatus(ctx, 2, f.group.ID, "idle", 0))
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "smoking", 0))
	f.now = f.now.Add(time.Minute)
	must(t, f.s.ChangeStatus(ctx, 1, f.group.ID, "idle", 0))
	stats, err = f.s.Statistics(ctx, 1, f.group.ID, "all")
	must(t, err)
	if stats.Leaderboard[0].UserID != 1 || stats.Leaderboard[1].UserID != 2 {
		t.Fatalf("unstable ties: %+v", stats.Leaderboard)
	}
}
