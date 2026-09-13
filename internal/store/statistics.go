package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// ReportLocation is shared by bot timestamps and the boundary of "today".
var ReportLocation = time.FixedZone("Красноярск", 7*60*60)

type SmokingTotals struct {
	Outings int64 `json:"outings"`
	Seconds int64 `json:"seconds"`
}

type PersonStatistics struct {
	UserID int64  `json:"user_id"`
	Name   string `json:"name"`
	SmokingTotals
}

type Statistics struct {
	Period         string             `json:"period"`
	From           int64              `json:"from"`
	ServerTime     int64              `json:"server_time"`
	Timezone       string             `json:"timezone"`
	Totals         SmokingTotals      `json:"totals"`
	Sessions       int64              `json:"sessions"`
	SessionSeconds int64              `json:"session_seconds"`
	Leaderboard    []PersonStatistics `json:"leaderboard"`
}

func statisticsStart(now time.Time, period string) (int64, error) {
	switch period {
	case "today":
		local := now.In(ReportLocation).Add(-6 * time.Hour)
		return time.Date(local.Year(), local.Month(), local.Day(), 6, 0, 0, 0, ReportLocation).Unix(), nil
	case "week":
		return now.Add(-7 * 24 * time.Hour).Unix(), nil
	case "month":
		return now.Add(-30 * 24 * time.Hour).Unix(), nil
	case "all":
		return 0, nil
	default:
		return 0, ErrInvalid
	}
}

// Statistics reports the caller's history across rooms, or a room's totals and
// current members. Counts use visit starts; durations are clipped to the period.
func (s *Store) Statistics(ctx context.Context, user int64, group, period string) (Statistics, error) {
	now := s.opts.Now()
	from, err := statisticsStart(now, period)
	result := Statistics{Period: period, From: from, ServerTime: now.Unix(), Timezone: "Asia/Krasnoyarsk", Leaderboard: []PersonStatistics{}}
	if err != nil {
		return result, err
	}
	err = s.transaction(ctx, func(tx *sql.Tx) error {
		if group != "" {
			if _, err := requireMember(tx, user, group); err != nil {
				return err
			}
		}
		// Keep arrival/expiry transitions consistent with the status screen.
		if err := s.tick(tx, result.ServerTime); err != nil {
			return err
		}
		filter := "v.user_id=?"
		var scope any = user
		if group != "" {
			filter, scope = "s.group_id=?", group
		}
		rows, err := tx.Query(`SELECT v.user_id,
		 SUM(CASE WHEN v.started_at>=? THEN 1 ELSE 0 END),
		 SUM(MAX(0,MIN(CASE WHEN v.ended_at=0 THEN ? ELSE v.ended_at END,?)-MAX(v.started_at,?)))
		 FROM session_visits v JOIN sessions s ON s.id=v.session_id
		 WHERE `+filter+` AND v.started_at<=? AND (v.ended_at=0 OR v.ended_at>=?) GROUP BY v.user_id`, from, result.ServerTime, result.ServerTime, from, scope, result.ServerTime, from)
		if err != nil {
			return err
		}
		people := map[int64]SmokingTotals{}
		for rows.Next() {
			var id int64
			var totals SmokingTotals
			if err = rows.Scan(&id, &totals.Outings, &totals.Seconds); err != nil {
				rows.Close()
				return err
			}
			people[id] = totals
			result.Totals.Outings += totals.Outings
			result.Totals.Seconds += totals.Seconds
		}
		err = rows.Err()
		rows.Close()
		if err != nil || group == "" {
			return err
		}
		members, err := members(tx, group)
		if err != nil {
			return err
		}
		for _, m := range members {
			result.Leaderboard = append(result.Leaderboard, PersonStatistics{UserID: m.ID, Name: m.FirstName, SmokingTotals: people[m.ID]})
		}
		sort.Slice(result.Leaderboard, func(i, j int) bool {
			a, b := result.Leaderboard[i], result.Leaderboard[j]
			if a.Outings != b.Outings {
				return a.Outings > b.Outings
			}
			if a.Seconds != b.Seconds {
				return a.Seconds > b.Seconds
			}
			return a.UserID < b.UserID
		})
		return tx.QueryRow(`SELECT COALESCE(SUM(CASE WHEN started_at>=? THEN 1 ELSE 0 END),0),
		 COALESCE(SUM(MAX(0,MIN(CASE WHEN ended_at=0 THEN ? ELSE ended_at END,?)-MAX(started_at,?))),0)
		 FROM sessions WHERE group_id=? AND started_at<=? AND (ended_at=0 OR ended_at>=?)`, from, result.ServerTime, result.ServerTime, from, group, result.ServerTime, from).Scan(&result.Sessions, &result.SessionSeconds)
	})
	return result, err
}
