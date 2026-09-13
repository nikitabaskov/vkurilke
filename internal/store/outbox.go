package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"
)

type Job struct {
	ID                                   int64
	Key, Kind, GroupID, SessionID, Token string
	UserID, ActorID, Revision            int64
	Attempts                             int
}
type Delivery struct {
	Job                   Job
	Skip, Closed, Revoked bool
	MessageID             int64
	GroupName, ActorName  string
	Members               []Member
	Visits                []Visit
	Duration              int64
}

// Visit is one person's total smoking time within a session.
type Visit struct {
	UserID  int64
	Name    string
	Seconds int64
	Present bool
}

func enqueue(tx *sql.Tx, j Job) error {
	_, err := tx.Exec(`INSERT INTO outbox(dedupe_key,kind,group_id,session_id,user_id,actor_id,token) VALUES(?,?,?,?,?,?,?)
	 ON CONFLICT(dedupe_key) DO UPDATE SET revision=outbox.revision+1,available_at=0,attempts=0`, j.Key, j.Kind, j.GroupID, j.SessionID, j.UserID, j.ActorID, j.Token)
	return err
}
func (s *Store) refreshCards(tx *sql.Tx, group string) error {
	rows, err := tx.Query(`SELECT m.session_id,m.user_id FROM session_messages m JOIN sessions s ON s.id=m.session_id WHERE s.group_id=?`, group)
	if err != nil {
		return err
	}
	jobs := []Job{}
	for rows.Next() {
		j := Job{Kind: "card", GroupID: group}
		if err = rows.Scan(&j.SessionID, &j.UserID); err != nil {
			rows.Close()
			return err
		}
		j.Key = "card:" + j.SessionID + ":" + strconv.FormatInt(j.UserID, 10)
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err = enqueue(tx, j); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) NextJob(ctx context.Context) (*Job, error) {
	var j Job
	err := s.db.QueryRowContext(ctx, `SELECT id,dedupe_key,kind,group_id,session_id,user_id,actor_id,token,revision,attempts FROM outbox WHERE available_at<=? ORDER BY id LIMIT 1`, s.now()).Scan(&j.ID, &j.Key, &j.Kind, &j.GroupID, &j.SessionID, &j.UserID, &j.ActorID, &j.Token, &j.Revision, &j.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &j, err
}

// Prepare rechecks access, preferences and session identity immediately before delivery.
func (s *Store) Prepare(ctx context.Context, j Job) (Delivery, error) {
	d := Delivery{Job: j}
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		var started, blocked bool
		var p Preferences
		if err := tx.QueryRow(`SELECT bot_started,blocked,notify_session,notify_arrival,notify_departure FROM users WHERE id=?`, j.UserID).Scan(&started, &blocked, &p.Session, &p.Arrival, &p.Departure); err != nil {
			return err
		}
		if !started || blocked {
			d.Skip = true
			return nil
		}
		_, err := requireMember(tx, j.UserID, j.GroupID)
		if errors.Is(err, ErrForbidden) {
			d.Revoked = true
		} else if err != nil {
			return err
		}
		if err = tx.QueryRow(`SELECT name FROM groups WHERE id=?`, j.GroupID).Scan(&d.GroupName); err != nil {
			return err
		}
		if j.Kind == "reminder" {
			var valid bool
			if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM smoker_statuses WHERE user_id=? AND group_id=? AND check_token=? AND check_deadline=0)`, j.UserID, j.GroupID, j.Token).Scan(&valid); err != nil {
				return err
			}
			d.Skip = !valid || d.Revoked
			return nil
		}
		var startedAt, ended int64
		err = tx.QueryRow(`SELECT started_at,ended_at FROM sessions WHERE id=?`, j.SessionID).Scan(&startedAt, &ended)
		if errors.Is(err, sql.ErrNoRows) {
			d.Skip = true
			return nil
		}
		if err != nil {
			return err
		}
		d.Closed = ended > 0
		if d.Closed {
			d.Duration = ended - startedAt
		}
		switch j.Kind {
		case "card":
			if err = tx.QueryRow(`SELECT message_id FROM session_messages WHERE session_id=? AND user_id=?`, j.SessionID, j.UserID).Scan(&d.MessageID); err != nil {
				return err
			}
			if d.MessageID == 0 && (!p.Session || d.Closed || d.Revoked) {
				d.Skip = true
				return nil
			}
			if d.Revoked {
				return nil
			}
			if !d.Closed {
				if d.Members, err = members(tx, j.GroupID); err != nil {
					return err
				}
			}
			d.Visits, err = visits(tx, j.SessionID, s.now())
			return err
		case "arrival", "departure":
			// Queued before the session card replaced separate arrival/departure messages.
			d.Skip = true
			return nil
		default:
			return ErrInvalid
		}
	})
	return d, err
}
func visits(tx *sql.Tx, session string, now int64) ([]Visit, error) {
	rows, err := tx.Query(`SELECT v.user_id,u.first_name,SUM(MAX(CASE WHEN v.ended_at=0 THEN ? ELSE v.ended_at END-v.started_at,0)),MAX(v.ended_at=0)
	 FROM session_visits v JOIN users u ON u.id=v.user_id WHERE v.session_id=? GROUP BY v.user_id ORDER BY MIN(v.started_at),v.user_id`, now, session)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Visit{}
	for rows.Next() {
		var v Visit
		if err = rows.Scan(&v.UserID, &v.Name, &v.Seconds, &v.Present); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) CompleteJob(ctx context.Context, j Job, messageID int64, delivered bool) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if delivered && j.Kind == "card" && messageID > 0 {
			if _, err := tx.Exec(`UPDATE session_messages SET message_id=? WHERE session_id=? AND user_id=?`, messageID, j.SessionID, j.UserID); err != nil {
				return err
			}
		}
		if delivered && j.Kind == "reminder" {
			if _, err := tx.Exec(`UPDATE smoker_statuses SET check_deadline=? WHERE user_id=? AND check_token=? AND check_deadline=0`, s.now()+int64(s.opts.AnswerTimeout/time.Second), j.UserID, j.Token); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM outbox WHERE id=? AND revision=?`, j.ID, j.Revision); err != nil {
			return err
		}
		if delivered {
			_, err := tx.Exec(`UPDATE outbox SET available_at=MAX(available_at,?) WHERE user_id=?`, s.now()+1, j.UserID)
			return err
		}
		return nil
	})
}
func (s *Store) RetryJob(ctx context.Context, j Job, delay time.Duration) error {
	_, err := s.db.ExecContext(ctx, `UPDATE outbox SET attempts=attempts+1,available_at=? WHERE id=? AND revision=?`, s.now()+int64(delay/time.Second)+1, j.ID, j.Revision)
	return err
}
func (s *Store) BlockBot(ctx context.Context, user int64) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`UPDATE users SET blocked=1 WHERE id=?`, user); err != nil {
			return err
		}
		var group string
		err := tx.QueryRow(`SELECT group_id FROM smoker_statuses WHERE user_id=?`, user).Scan(&group)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// Without a delivery channel, presence cannot be checked; do not leave it stuck on.
		if group != "" {
			if err = s.leaveStatus(tx, user, group, s.now()); err != nil {
				return err
			}
		}
		_, err = tx.Exec(`DELETE FROM outbox WHERE user_id=?`, user)
		return err
	})
}
func (s *Store) BotOffset(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT value FROM bot_state WHERE key='offset'`).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}
func (s *Store) SetBotOffset(ctx context.Context, n int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO bot_state(key,value) VALUES('offset',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, n)
	return err
}
