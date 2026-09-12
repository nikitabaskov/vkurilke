package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Status struct {
	GroupID       string `json:"group_id"`
	Status        string `json:"status"`
	StartedAt     int64  `json:"started_at"`
	TargetTime    int64  `json:"target_time"`
	CheckToken    string `json:"check_token,omitempty"`
	CheckDeadline int64  `json:"check_deadline"`
}
type Member struct {
	User
	Role       string `json:"role"`
	Status     string `json:"status"`
	StartedAt  int64  `json:"started_at"`
	TargetTime int64  `json:"target_time"`
}
type Snapshot struct {
	Members    []Member `json:"members"`
	Active     *Status  `json:"active"`
	ServerTime int64    `json:"server_time"`
}

func (s *Store) Snapshot(ctx context.Context, user int64, group string) (Snapshot, error) {
	state := Snapshot{Members: []Member{}, ServerTime: s.now()}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := requireMember(tx, user, group); err != nil {
			return err
		}
		var err error
		state.Members, err = members(tx, group)
		if err != nil {
			return err
		}
		var active Status
		err = tx.QueryRow(`SELECT group_id,status,started_at,target_time,check_token,check_deadline FROM smoker_statuses WHERE user_id=?`, user).Scan(&active.GroupID, &active.Status, &active.StartedAt, &active.TargetTime, &active.CheckToken, &active.CheckDeadline)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		state.Active = &active
		return nil
	})
	return state, err
}
func members(tx *sql.Tx, group string) ([]Member, error) {
	rows, err := tx.Query(`SELECT u.id,u.username,u.first_name,u.photo_url,u.bot_started,m.role,COALESCE(s.status,'idle'),COALESCE(s.started_at,0),COALESCE(s.target_time,0)
	 FROM memberships m JOIN users u ON u.id=m.user_id LEFT JOIN smoker_statuses s ON s.user_id=u.id AND s.group_id=m.group_id
	 WHERE m.group_id=? AND m.banned=0 ORDER BY CASE COALESCE(s.status,'idle') WHEN 'smoking' THEN 0 WHEN 'going' THEN 1 ELSE 2 END, s.started_at,u.first_name,u.id`, group)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Member{}
	for rows.Next() {
		var m Member
		if err = rows.Scan(&m.ID, &m.Username, &m.FirstName, &m.PhotoURL, &m.BotStarted, &m.Role, &m.Status, &m.StartedAt, &m.TargetTime); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ChangeStatus(ctx context.Context, user int64, group, status string, duration int) error {
	if status != "idle" && status != "going" && status != "smoking" {
		return ErrInvalid
	}
	if status == "going" && duration != 3 && duration != 5 && duration != 10 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := requireMember(tx, user, group); err != nil {
			return err
		}
		if err := s.tick(tx, s.now()); err != nil {
			return err
		}
		return s.change(tx, user, group, status, duration, s.now())
	})
}
func (s *Store) change(tx *sql.Tx, user int64, group, status string, duration int, now int64) error {
	var oldGroup, oldStatus string
	err := tx.QueryRow(`SELECT group_id,status FROM smoker_statuses WHERE user_id=?`, user).Scan(&oldGroup, &oldStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if status == "idle" {
		return s.leaveStatus(tx, user, group, now)
	}
	var reachable bool
	if err = tx.QueryRow(`SELECT bot_started AND NOT blocked FROM users WHERE id=?`, user).Scan(&reachable); err != nil {
		return err
	}
	if !reachable {
		return ErrBotUnavailable
	}
	if oldGroup == group && (oldStatus == status || (oldStatus == "smoking" && status == "going")) {
		return nil
	}
	if oldGroup != "" && oldGroup != group {
		if err = s.leaveStatus(tx, user, oldGroup, now); err != nil {
			return err
		}
	}
	episode := randomID()
	var target, next int64
	if status == "going" {
		target = now + int64(duration*60)
	} else {
		next = now + int64(s.opts.CheckInterval/time.Second)
	}
	_, err = tx.Exec(`INSERT INTO smoker_statuses(user_id,group_id,status,episode_id,started_at,target_time,next_check_at) VALUES(?,?,?,?,?,?,?)
	 ON CONFLICT(user_id) DO UPDATE SET group_id=excluded.group_id,status=excluded.status,episode_id=excluded.episode_id,started_at=excluded.started_at,target_time=excluded.target_time,next_check_at=excluded.next_check_at,check_token='',check_deadline=0`, user, group, status, episode, now, target, next)
	if err != nil {
		return err
	}
	var session string
	err = tx.QueryRow(`SELECT id FROM sessions WHERE group_id=? AND ended_at=0`, group).Scan(&session)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if session == "" && status == "smoking" {
		session = randomID()
		if _, err = tx.Exec(`INSERT INTO sessions(id,group_id,founder_id,started_at) VALUES(?,?,?,?)`, session, group, user, now); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO session_messages(session_id,user_id) SELECT ?,u.id FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.group_id=? AND m.banned=0 AND u.bot_started=1 AND u.blocked=0 AND u.notify_session=1 AND u.id<>?`, session, group, user); err != nil {
			return err
		}
	} else if status == "smoking" && session != "" {
		if err = s.enqueueAudience(tx, "arrival", group, session, user, episode); err != nil {
			return err
		}
	}
	return s.refreshCards(tx, group)
}
func (s *Store) leaveStatus(tx *sql.Tx, user int64, group string, now int64) error {
	var episode string
	err := tx.QueryRow(`SELECT episode_id FROM smoker_statuses WHERE user_id=? AND group_id=?`, user, group).Scan(&episode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM smoker_statuses WHERE user_id=? AND group_id=?`, user, group); err != nil {
		return err
	}
	var session string
	err = tx.QueryRow(`SELECT id FROM sessions WHERE group_id=? AND ended_at=0`, group).Scan(&session)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if session != "" {
		if err = s.enqueueAudience(tx, "departure", group, session, user, episode); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE sessions SET ended_at=? WHERE group_id=? AND ended_at=0 AND NOT EXISTS(SELECT 1 FROM smoker_statuses WHERE group_id=?)`, now, group, group); err != nil {
		return err
	}
	return s.refreshCards(tx, group)
}

func (s *Store) JoinSession(ctx context.Context, user int64, session string) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := s.tick(tx, s.now()); err != nil {
			return err
		}
		var group string
		if err := tx.QueryRow(`SELECT group_id FROM sessions WHERE id=? AND ended_at=0`, session).Scan(&group); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrStale
			}
			return err
		}
		if _, err := requireMember(tx, user, group); err != nil {
			return err
		}
		return s.change(tx, user, group, "going", 5, s.now())
	})
}
func (s *Store) Confirm(ctx context.Context, user int64, token string, stillHere bool) error {
	if token == "" {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var group string
		if err := tx.QueryRow(`SELECT group_id FROM smoker_statuses WHERE user_id=? AND status='smoking' AND check_token=? AND (check_deadline=0 OR check_deadline>?)`, user, token, s.now()).Scan(&group); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrStale
			}
			return err
		}
		if _, err := requireMember(tx, user, group); err != nil {
			return err
		}
		if !stillHere {
			return s.leaveStatus(tx, user, group, s.now())
		}
		_, err := tx.Exec(`UPDATE smoker_statuses SET check_token='',check_deadline=0,next_check_at=? WHERE user_id=?`, s.now()+int64(s.opts.CheckInterval/time.Second), user)
		return err
	})
}
func (s *Store) Tick(ctx context.Context) error {
	return s.transaction(ctx, func(tx *sql.Tx) error { return s.tick(tx, s.now()) })
}
func (s *Store) tick(tx *sql.Tx, now int64) error {
	rows, err := tx.Query(`SELECT user_id,group_id,status,target_time,check_token,check_deadline FROM smoker_statuses WHERE (status='going' AND target_time<=?) OR (status='smoking' AND ((check_token='' AND next_check_at<=?) OR (check_deadline>0 AND check_deadline<=?)))`, now, now, now)
	if err != nil {
		return err
	}
	type due struct {
		user                 int64
		group, status, token string
		target, deadline     int64
	}
	list := []due{}
	for rows.Next() {
		var d due
		if err = rows.Scan(&d.user, &d.group, &d.status, &d.target, &d.token, &d.deadline); err != nil {
			rows.Close()
			return err
		}
		list = append(list, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, d := range list {
		switch {
		case d.status == "going":
			// Anchor arrival to its deadline, not a delayed worker/restart time.
			if err = s.change(tx, d.user, d.group, "smoking", 0, d.target); err != nil {
				return err
			}
		case d.deadline > 0:
			if err = s.leaveStatus(tx, d.user, d.group, now); err != nil {
				return err
			}
		default:
			token := randomID()
			if _, err = tx.Exec(`UPDATE smoker_statuses SET check_token=? WHERE user_id=?`, token, d.user); err != nil {
				return err
			}
			if err = enqueue(tx, Job{Key: "reminder:" + token, Kind: "reminder", GroupID: d.group, UserID: d.user, Token: token}); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`DELETE FROM auth_sessions WHERE expires_at<=?`, now); err != nil {
		return err
	}
	_, err = tx.Exec(`DELETE FROM sessions WHERE ended_at>0 AND NOT EXISTS(SELECT 1 FROM outbox WHERE outbox.session_id=sessions.id)`)
	return err
}
