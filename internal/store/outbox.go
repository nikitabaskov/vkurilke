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
}

func enqueue(tx *sql.Tx, j Job) error {
	_, err := tx.Exec(`INSERT INTO outbox(dedupe_key,kind,group_id,session_id,user_id,actor_id,token) VALUES(?,?,?,?,?,?,?)
	 ON CONFLICT(dedupe_key) DO UPDATE SET revision=outbox.revision+1,available_at=0,attempts=0`, j.Key, j.Kind, j.GroupID, j.SessionID, j.UserID, j.ActorID, j.Token)
	return err
}
func (s *Store) enqueueAudience(tx *sql.Tx, kind, group, session string, actor int64, episode string) error {
	column := "notify_arrival"
	if kind == "departure" {
		column = "notify_departure"
	}
	rows, err := tx.Query(`SELECT u.id FROM memberships m JOIN users u ON u.id=m.user_id WHERE m.group_id=? AND m.banned=0 AND u.bot_started=1 AND u.blocked=0 AND u.`+column+`=1 AND u.id<>?`, group, actor)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = enqueue(tx, Job{Key: kind + ":" + episode + ":" + strconv.FormatInt(id, 10), Kind: kind, GroupID: group, SessionID: session, UserID: id, ActorID: actor, Token: episode}); err != nil {
			return err
		}
	}
	return nil
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
		var ended int64
		err = tx.QueryRow(`SELECT ended_at FROM sessions WHERE id=?`, j.SessionID).Scan(&ended)
		if errors.Is(err, sql.ErrNoRows) {
			d.Skip = true
			return nil
		}
		if err != nil {
			return err
		}
		d.Closed = ended > 0
		switch j.Kind {
		case "card":
			if err = tx.QueryRow(`SELECT message_id FROM session_messages WHERE session_id=? AND user_id=?`, j.SessionID, j.UserID).Scan(&d.MessageID); err != nil {
				return err
			}
			if d.MessageID == 0 && (!p.Session || d.Closed || d.Revoked) {
				d.Skip = true
				return nil
			}
			if !d.Closed && !d.Revoked {
				d.Members, err = members(tx, j.GroupID)
			}
			return err
		case "arrival", "departure":
			if d.Revoked || (j.Kind == "arrival" && (!p.Arrival || d.Closed)) || (j.Kind == "departure" && !p.Departure) {
				d.Skip = true
				return nil
			}
			if j.Kind == "arrival" {
				var valid bool
				if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM smoker_statuses WHERE user_id=? AND episode_id=? AND status='smoking')`, j.ActorID, j.Token).Scan(&valid); err != nil {
					return err
				}
				if !valid {
					d.Skip = true
					return nil
				}
			}
			return tx.QueryRow(`SELECT first_name FROM users WHERE id=?`, j.ActorID).Scan(&d.ActorName)
		default:
			return ErrInvalid
		}
	})
	return d, err
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
