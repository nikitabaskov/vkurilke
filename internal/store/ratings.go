package store

import (
	"context"
	"database/sql"
	"errors"
)

// RateSession records one replaceable vote per participant of a completed session.
func (s *Store) RateSession(ctx context.Context, user int64, session string, value int) error {
	if value != -1 && value != 1 {
		return ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var group string
		var ended int64
		if err := tx.QueryRow(`SELECT group_id,ended_at FROM sessions WHERE id=?`, session).Scan(&group, &ended); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrStale
			}
			return err
		}
		if _, err := requireMember(tx, user, group); err != nil {
			return err
		}
		if ended == 0 {
			return ErrInvalid
		}
		var participated bool
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM session_visits WHERE session_id=? AND user_id=?)`, session, user).Scan(&participated); err != nil {
			return err
		}
		if !participated {
			return ErrForbidden
		}
		result, err := tx.Exec(`INSERT INTO session_ratings(session_id,user_id,value) VALUES(?,?,?)
		 ON CONFLICT(session_id,user_id) DO UPDATE SET value=excluded.value WHERE session_ratings.value<>excluded.value`, session, user, value)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil || n == 0 {
			return err
		}
		// Refresh only existing copies of this session, through the durable delivery queue.
		_, err = tx.Exec(`INSERT INTO outbox(dedupe_key,kind,group_id,session_id,user_id)
		 SELECT 'card:' || session_id || ':' || user_id,'card',?,session_id,user_id FROM session_messages WHERE session_id=? AND message_id>0
		 ON CONFLICT(dedupe_key) DO UPDATE SET revision=outbox.revision+1,available_at=0,attempts=0`, group, session)
		return err
	})
}
