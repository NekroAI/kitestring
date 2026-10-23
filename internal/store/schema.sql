-- Kitestring Hub 数据库结构（版本 1）。
-- 时间统一存 RFC3339Nano UTC 文本；JSON 字段存文本。

CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS web_sessions (
	token_hash TEXT PRIMARY KEY,
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS workspaces (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	notify     TEXT NOT NULL DEFAULT '{}', -- 通知渠道配置 JSON
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS devices (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL UNIQUE,
	token_hash  TEXT NOT NULL UNIQUE,
	hostname    TEXT NOT NULL DEFAULT '',
	platform    TEXT NOT NULL DEFAULT '',
	version     TEXT NOT NULL DEFAULT '',
	revoked     INTEGER NOT NULL DEFAULT 0,
	online      INTEGER NOT NULL DEFAULT 0,
	last_seen   TEXT,
	created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS projects (
	id           TEXT PRIMARY KEY,
	name         TEXT NOT NULL,
	remote       TEXT NOT NULL DEFAULT '', -- 归一化后的 git remote，空表示按设备+路径
	workspace_id TEXT REFERENCES workspaces(id) ON DELETE SET NULL,
	doc_root     TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS projects_remote ON projects(remote) WHERE remote <> '';

CREATE TABLE IF NOT EXISTS project_paths (
	device_id  TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	path       TEXT NOT NULL,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	PRIMARY KEY (device_id, path)
);

CREATE TABLE IF NOT EXISTS sessions (
	key            TEXT PRIMARY KEY, -- tool:session_id
	tool           TEXT NOT NULL,
	session_id     TEXT NOT NULL,
	device_id      TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
	project_id     TEXT REFERENCES projects(id) ON DELETE SET NULL,
	client         TEXT NOT NULL DEFAULT '',
	model          TEXT NOT NULL DEFAULT '',
	cwd            TEXT NOT NULL DEFAULT '',
	branch         TEXT NOT NULL DEFAULT '',
	dirty          INTEGER NOT NULL DEFAULT 0,
	state          TEXT NOT NULL,
	state_detail   TEXT NOT NULL DEFAULT '',
	last_prompt    TEXT NOT NULL DEFAULT '',
	summary        TEXT NOT NULL DEFAULT '',
	state_since    TEXT NOT NULL DEFAULT '',
	turns          INTEGER NOT NULL DEFAULT 0,
	turn_tools     INTEGER NOT NULL DEFAULT 0,
	last_tool      TEXT NOT NULL DEFAULT '',
	last_tool_at   TEXT NOT NULL DEFAULT '',
	subagents      INTEGER NOT NULL DEFAULT 0,
	perm_mode      TEXT NOT NULL DEFAULT '',
	compactions    INTEGER NOT NULL DEFAULT 0,
	worktree       INTEGER NOT NULL DEFAULT 0,
	title          TEXT NOT NULL DEFAULT '',
	phase          TEXT NOT NULL DEFAULT '',
	digest_done    TEXT NOT NULL DEFAULT '',
	digest_ask     TEXT NOT NULL DEFAULT '',
	started_at     TEXT NOT NULL,
	last_active_at TEXT NOT NULL,
	ended_at       TEXT
);
CREATE INDEX IF NOT EXISTS sessions_device ON sessions(device_id);

CREATE TABLE IF NOT EXISTS events (
	id          TEXT PRIMARY KEY, -- ULID，幂等
	device_id   TEXT NOT NULL,
	session_key TEXT NOT NULL,
	name        TEXT NOT NULL,
	time        TEXT NOT NULL,
	payload     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_session ON events(session_key, time);
CREATE INDEX IF NOT EXISTS events_time ON events(time);

CREATE TABLE IF NOT EXISTS requirements (
	id          TEXT PRIMARY KEY,
	seq         INTEGER NOT NULL UNIQUE, -- 人类可读编号 R-<seq>
	project_id  TEXT REFERENCES projects(id) ON DELETE SET NULL,
	title       TEXT NOT NULL,
	detail      TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL,
	priority    TEXT NOT NULL DEFAULT 'P2',
	source      TEXT NOT NULL DEFAULT '',
	acceptance  TEXT NOT NULL DEFAULT '',
	doc_link    TEXT NOT NULL DEFAULT '',
	created_by  TEXT NOT NULL DEFAULT '',
	created_at  TEXT NOT NULL,
	updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS requirement_sessions (
	requirement_id TEXT NOT NULL REFERENCES requirements(id) ON DELETE CASCADE,
	session_key    TEXT NOT NULL REFERENCES sessions(key) ON DELETE CASCADE,
	linked_at      TEXT NOT NULL,
	PRIMARY KEY (requirement_id, session_key)
);

CREATE TABLE IF NOT EXISTS decisions (
	id             TEXT PRIMARY KEY,
	seq            INTEGER NOT NULL UNIQUE, -- D-<seq>
	session_key    TEXT, -- 目标会话（D15），可被用户转交
	origin_session TEXT,
	project_id     TEXT REFERENCES projects(id) ON DELETE SET NULL,
	requirement_id TEXT REFERENCES requirements(id) ON DELETE SET NULL,
	question       TEXT NOT NULL,
	context        TEXT NOT NULL DEFAULT '',
	options        TEXT NOT NULL DEFAULT '[]',
	recommendation TEXT NOT NULL DEFAULT '',
	short_title    TEXT NOT NULL DEFAULT '',
	status         TEXT NOT NULL, -- pending | answered | delivered | closed
	answer         TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL,
	answered_at    TEXT,
	delivered_at   TEXT,
	delivered_turn TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS decisions_status ON decisions(status);

CREATE TABLE IF NOT EXISTS files (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	mime       TEXT NOT NULL,
	size       INTEGER NOT NULL,
	sha256     TEXT NOT NULL,
	uploader   TEXT NOT NULL, -- user | device:<id>
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS evidence (
	id             TEXT PRIMARY KEY,
	requirement_id TEXT NOT NULL REFERENCES requirements(id) ON DELETE CASCADE,
	session_key    TEXT,
	kind           TEXT NOT NULL,
	summary        TEXT NOT NULL,
	link           TEXT NOT NULL DEFAULT '',
	voided         INTEGER NOT NULL DEFAULT 0,
	created_at     TEXT NOT NULL
);

-- 计划（R-43）：需求之上的一层，按顺序分阶段推进、逐阶段验收。
CREATE TABLE IF NOT EXISTS plans (
	id            TEXT PRIMARY KEY,
	seq           INTEGER NOT NULL UNIQUE, -- P-<seq>
	project_id    TEXT REFERENCES projects(id) ON DELETE SET NULL,
	title         TEXT NOT NULL,
	goal          TEXT NOT NULL DEFAULT '',
	owner_session TEXT NOT NULL DEFAULT '', -- 负责会话：阶段解锁后需求自动交给它
	status        TEXT NOT NULL, -- draft | active | done | shelved
	created_by    TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plan_stages (
	id         TEXT PRIMARY KEY,
	plan_id    TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
	seq        INTEGER NOT NULL, -- 阶段顺序，从 1 开始
	title      TEXT NOT NULL,
	status     TEXT NOT NULL, -- locked | active | review | passed
	summary    TEXT NOT NULL DEFAULT '', -- Agent 交的阶段小结
	summary_at TEXT NOT NULL DEFAULT '',
	passed_at  TEXT NOT NULL DEFAULT ''
);
