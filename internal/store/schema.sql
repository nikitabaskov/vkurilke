PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;

CREATE TABLE IF NOT EXISTS users (
 id INTEGER PRIMARY KEY, username TEXT NOT NULL DEFAULT '', first_name TEXT NOT NULL,
 photo_url TEXT NOT NULL DEFAULT '', bot_started INTEGER NOT NULL DEFAULT 0,
 blocked INTEGER NOT NULL DEFAULT 0,
 notify_session INTEGER NOT NULL DEFAULT 1, notify_arrival INTEGER NOT NULL DEFAULT 1,
 notify_departure INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS groups (
 id TEXT PRIMARY KEY, name TEXT NOT NULL, invite_code TEXT NOT NULL UNIQUE, created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS memberships (
 group_id TEXT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id),
 role TEXT NOT NULL CHECK(role IN ('owner','admin','member')) DEFAULT 'member',
 banned INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(group_id,user_id)
);
CREATE UNIQUE INDEX IF NOT EXISTS group_owner ON memberships(group_id) WHERE role='owner';
CREATE INDEX IF NOT EXISTS member_rooms ON memberships(user_id,banned);
CREATE TABLE IF NOT EXISTS sessions (
 id TEXT PRIMARY KEY, group_id TEXT NOT NULL REFERENCES groups(id), founder_id INTEGER NOT NULL REFERENCES users(id),
 started_at INTEGER NOT NULL, ended_at INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS active_session ON sessions(group_id) WHERE ended_at=0;
CREATE TABLE IF NOT EXISTS smoker_statuses (
 user_id INTEGER PRIMARY KEY REFERENCES users(id), group_id TEXT NOT NULL REFERENCES groups(id),
 status TEXT NOT NULL CHECK(status IN ('going','smoking')), episode_id TEXT NOT NULL,
 started_at INTEGER NOT NULL, target_time INTEGER NOT NULL DEFAULT 0,
 next_check_at INTEGER NOT NULL DEFAULT 0, check_token TEXT NOT NULL DEFAULT '', check_deadline INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS room_statuses ON smoker_statuses(group_id,status);
CREATE TABLE IF NOT EXISTS auth_sessions (
 token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id), expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS auth_expiry ON auth_sessions(expires_at);
CREATE TABLE IF NOT EXISTS session_messages (
 session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 user_id INTEGER NOT NULL REFERENCES users(id), message_id INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(session_id,user_id)
);
CREATE TABLE IF NOT EXISTS outbox (
 id INTEGER PRIMARY KEY AUTOINCREMENT, dedupe_key TEXT NOT NULL UNIQUE,
 kind TEXT NOT NULL, group_id TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '',
 user_id INTEGER NOT NULL REFERENCES users(id), actor_id INTEGER NOT NULL DEFAULT 0,
 token TEXT NOT NULL DEFAULT '', revision INTEGER NOT NULL DEFAULT 1,
 attempts INTEGER NOT NULL DEFAULT 0, available_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS outbox_due ON outbox(available_at,id);
CREATE TABLE IF NOT EXISTS bot_state (key TEXT PRIMARY KEY, value INTEGER NOT NULL);
PRAGMA user_version=1;
