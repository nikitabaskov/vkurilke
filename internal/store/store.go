package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var (
	ErrForbidden       = errors.New("Нет доступа к комнате или действию")
	ErrInvalid         = errors.New("Проверьте введенные данные")
	ErrNotFound        = errors.New("Не найдено")
	ErrStale           = errors.New("Этот сеанс или запрос уже завершен")
	ErrUnauthenticated = errors.New("Откройте приложение заново через Telegram")
	ErrBotUnavailable  = errors.New("Сначала запустите бота командой /start, чтобы получать проверку присутствия")
)

type Options struct {
	AdminIDs                     []int64
	CheckInterval, AnswerTimeout time.Duration
	Now                          func() time.Time
}
type Store struct {
	db   *sql.DB
	opts Options
}

// checkWritable turns a permission problem into a readable message: the SQLite
// driver reports a file it cannot create or open as "out of memory (14)", which
// sends people looking for a memory limit instead of the data directory owner.
func checkWritable(path string) error {
	if f, err := os.OpenFile(path, os.O_RDWR, 0600); err == nil {
		f.Close()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("database file %s is not writable: %w", path, err)
	}
	// SQLite also writes journal, WAL and SHM files next to the database.
	dir := filepath.Dir(path)
	probe, err := os.CreateTemp(dir, ".vkurilke-write-*")
	if err != nil {
		return fmt.Errorf("database directory %s is not writable: %w", dir, err)
	}
	probe.Close()
	return os.Remove(probe.Name())
}
func Open(path string, opts Options) (*Store, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.CheckInterval <= 0 {
		opts.CheckInterval = 15 * time.Minute
	}
	if opts.AnswerTimeout <= 0 {
		opts.AnswerTimeout = 3 * time.Minute
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := checkWritable(path); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	q := url.Values{"_pragma": {"foreign_keys(1)", "busy_timeout(5000)"}}
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	// One connection serializes short transactions; no Telegram calls run inside them.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var version int
	if err = db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	if version > 1 {
		db.Close()
		return nil, errors.New("database schema is newer than this application")
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	// Sessions that were active before visits were recorded still list their current smokers.
	if _, err = db.Exec(`INSERT OR IGNORE INTO session_visits(episode_id,session_id,user_id,started_at) SELECT st.episode_id,s.id,st.user_id,st.started_at FROM smoker_statuses st JOIN sessions s ON s.group_id=st.group_id AND s.ended_at=0 WHERE st.status='smoking'`); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, opts: opts}, nil
}
func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
func (s *Store) now() int64                     { return s.opts.Now().Unix() }
func (s *Store) IsAdmin(id int64) bool {
	for _, a := range s.opts.AdminIDs {
		if id == a {
			return true
		}
	}
	return false
}
func (s *Store) transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
func randomID() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func tokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

type User struct {
	ID         int64  `json:"id"`
	Username   string `json:"username"`
	FirstName  string `json:"first_name"`
	PhotoURL   string `json:"photo_url"`
	BotStarted bool   `json:"bot_started"`
}
type Preferences struct {
	Session   bool `json:"session"`
	Arrival   bool `json:"arrival"`
	Departure bool `json:"departure"`
}

func (s *Store) UpsertUser(ctx context.Context, u User, fromBot bool) error {
	if u.ID <= 0 || u.FirstName == "" {
		return ErrInvalid
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO users(id,username,first_name,photo_url,bot_started,created_at) VALUES(?,?,?,?,?,?)
	 ON CONFLICT(id) DO UPDATE SET username=excluded.username, first_name=excluded.first_name,
	 photo_url=CASE WHEN excluded.photo_url='' THEN users.photo_url ELSE excluded.photo_url END,
	 bot_started=MAX(users.bot_started,excluded.bot_started), blocked=CASE WHEN excluded.bot_started=1 THEN 0 ELSE users.blocked END`, u.ID, u.Username, u.FirstName, u.PhotoURL, fromBot, s.now())
	return err
}
func (s *Store) User(ctx context.Context, id int64) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id,username,first_name,photo_url,bot_started AND NOT blocked FROM users WHERE id=?`, id).Scan(&u.ID, &u.Username, &u.FirstName, &u.PhotoURL, &u.BotStarted)
	return u, err
}
func (s *Store) Preferences(ctx context.Context, id int64) (Preferences, error) {
	var p Preferences
	err := s.db.QueryRowContext(ctx, `SELECT notify_session,notify_arrival,notify_departure FROM users WHERE id=?`, id).Scan(&p.Session, &p.Arrival, &p.Departure)
	return p, err
}
func (s *Store) SetPreferences(ctx context.Context, id int64, p Preferences) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET notify_session=?,notify_arrival=?,notify_departure=? WHERE id=?`, p.Session, p.Arrival, p.Departure, id)
	return err
}
func (s *Store) ToggleNotifications(ctx context.Context, id int64) (Preferences, error) {
	return s.ToggleNotificationsForUpdate(ctx, id, 0)
}

// A replayed Telegram command must not toggle settings a second time after a send failure.
func (s *Store) ToggleNotificationsForUpdate(ctx context.Context, id, updateID int64) (Preferences, error) {
	var p Preferences
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		key := "toggle:" + strconv.FormatInt(id, 10)
		if updateID > 0 {
			var previous int64
			err := tx.QueryRow(`SELECT value FROM bot_state WHERE key=?`, key).Scan(&previous)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if previous >= updateID {
				return tx.QueryRow(`SELECT notify_session,notify_arrival,notify_departure FROM users WHERE id=?`, id).Scan(&p.Session, &p.Arrival, &p.Departure)
			}
		}
		var enabled bool
		if err := tx.QueryRow(`SELECT notify_session FROM users WHERE id=?`, id).Scan(&enabled); err != nil {
			return err
		}
		p = Preferences{Session: !enabled, Arrival: !enabled}
		_, err := tx.Exec(`UPDATE users SET notify_session=?,notify_arrival=?,notify_departure=0 WHERE id=?`, p.Session, p.Arrival, id)
		if err == nil && updateID > 0 {
			_, err = tx.Exec(`INSERT INTO bot_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, updateID)
		}
		return err
	})
	return p, err
}
func (s *Store) NewAuthSession(ctx context.Context, id int64) (string, error) {
	token := randomID() + randomID()
	_, err := s.db.ExecContext(ctx, `INSERT INTO auth_sessions(token_hash,user_id,expires_at) VALUES(?,?,?)`, tokenHash(token), id, s.now()+86400)
	return token, err
}
func (s *Store) Authenticate(ctx context.Context, token string) (int64, error) {
	if len(token) != 48 {
		return 0, ErrUnauthenticated
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT user_id FROM auth_sessions WHERE token_hash=? AND expires_at>?`, tokenHash(token), s.now()).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrUnauthenticated
		}
		return 0, err
	}
	return id, nil
}
func requireMember(tx *sql.Tx, user int64, group string) (string, error) {
	var role string
	if err := tx.QueryRow(`SELECT role FROM memberships WHERE user_id=? AND group_id=? AND banned=0`, user, group).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrForbidden
		}
		return "", err
	}
	return role, nil
}
func affected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
