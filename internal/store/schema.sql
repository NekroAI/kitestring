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
	phase          TEXT NOT NULL DEFAULT '', -- 已停用（R-125）：不再让模型猜阶段
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

-- 浏览器系统推送订阅（R-49 Web Push）：一台设备上的一个浏览器 / 主屏 App 一条
CREATE TABLE IF NOT EXISTS push_subs (
	id         TEXT PRIMARY KEY,
	endpoint   TEXT NOT NULL UNIQUE,
	p256dh     TEXT NOT NULL,
	auth       TEXT NOT NULL,
	name       TEXT NOT NULL DEFAULT '', -- 设备说明，如“iPhone · 主屏 App”
	origin     TEXT NOT NULL DEFAULT '', -- 订阅时的网页来源，作为 VAPID 联系方式
	created_at TEXT NOT NULL,
	last_ok_at TEXT NOT NULL DEFAULT ''
);

-- passkey（R-7）：只存公钥与计数器等验证所需的数据（webauthn.Credential 的 JSON），不存任何秘密
CREATE TABLE IF NOT EXISTS passkeys (
	id           TEXT PRIMARY KEY, -- 凭据 ID（base64url）
	name         TEXT NOT NULL DEFAULT '',
	origin       TEXT NOT NULL,     -- 注册时的网页来源，登录只接受这些来源
	rp_id        TEXT NOT NULL,
	credential   TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	last_used_at TEXT NOT NULL DEFAULT ''
);

-- 参谋授权窗口（R-163）：你在网页上授权一个 Adv 会话在到期前代你答复决策（风险档不超过 max_risk）；
-- 开启、撤销都记在这里，到期按 expires_at 判断，不删除，作为留痕
CREATE TABLE IF NOT EXISTS grants (
	id          TEXT PRIMARY KEY,
	seq         INTEGER NOT NULL UNIQUE, -- G-<seq>
	project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	session_key TEXT NOT NULL,
	max_risk    TEXT NOT NULL, -- low | medium
	expires_at  TEXT NOT NULL,
	created_at  TEXT NOT NULL,
	revoked_at  TEXT NOT NULL DEFAULT ''
);

-- 离开期间小结（R-165）：Adv 写给你的总述（每个项目一份，覆盖写入）与逐条验收建议（每条需求一份，覆盖写入）。
-- 其余内容（代答、放行、待验收、未答决策、受阻、团队消息）由 hub 从已有记录自动汇总，不另存
CREATE TABLE IF NOT EXISTS morning_notes (
	project_id TEXT PRIMARY KEY REFERENCES projects(id) ON DELETE CASCADE,
	text       TEXT NOT NULL,
	by_session TEXT NOT NULL,
	at         TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS review_advice (
	requirement_id TEXT PRIMARY KEY REFERENCES requirements(id) ON DELETE CASCADE,
	verdict        TEXT NOT NULL, -- pass | return
	reason         TEXT NOT NULL,
	by_session     TEXT NOT NULL,
	at             TEXT NOT NULL
);

-- 需求状态变更历史（R-153）：每次换状态一行。actor：user 你 / agent 会话 / system 系统；via 是经由（网页、Agent 工具、放行……）；
-- ref 是关联记录的编号（放行、退回修改等，D-x）。只从上线起记录
CREATE TABLE IF NOT EXISTS status_changes (
	id             TEXT PRIMARY KEY,
	requirement_id TEXT NOT NULL REFERENCES requirements(id) ON DELETE CASCADE,
	from_status    TEXT NOT NULL,
	to_status      TEXT NOT NULL,
	actor          TEXT NOT NULL,
	session_key    TEXT NOT NULL DEFAULT '',
	role           TEXT NOT NULL DEFAULT '',
	via            TEXT NOT NULL DEFAULT '',
	ref            TEXT NOT NULL DEFAULT '',
	at             TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS status_changes_req ON status_changes(requirement_id, at);
CREATE INDEX IF NOT EXISTS status_changes_at ON status_changes(at);

-- Hub 自己的模型调用计量（R-120）：每次接口调用一行——功能、模型、成败、接口返回的 token 用量（has_usage = 0 表示接口没返回）、耗时。
-- 只记计量，不存提示词或输出，也不读用户对话内容。只从上线起记录，保留 35 天。
CREATE TABLE IF NOT EXISTS ai_calls (
	id          TEXT PRIMARY KEY,
	feature     TEXT NOT NULL,
	model       TEXT NOT NULL DEFAULT '',
	ok          INTEGER NOT NULL,
	prompt_tok  INTEGER NOT NULL DEFAULT 0,
	compl_tok   INTEGER NOT NULL DEFAULT 0,
	has_usage   INTEGER NOT NULL DEFAULT 0,
	duration_ms INTEGER NOT NULL DEFAULT 0,
	at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ai_calls_at ON ai_calls(at);

-- 停止后台进程的留痕（R-209）：谁、什么时候、哪台设备、哪个进程、结果。进程以“进程号@启动时间”认（proc_id）；
-- signal：SIGTERM / SIGKILL，没发信号时为空；result：exited / running / killed / failed / refused / gone
CREATE TABLE IF NOT EXISTS proc_stops (
	id          TEXT PRIMARY KEY,
	device_id   TEXT NOT NULL,
	device_name TEXT NOT NULL DEFAULT '',
	proc_id     TEXT NOT NULL,
	pid         INTEGER NOT NULL,
	cmd         TEXT NOT NULL DEFAULT '',
	project     TEXT NOT NULL DEFAULT '',
	session_key TEXT NOT NULL DEFAULT '',
	state       TEXT NOT NULL DEFAULT '',
	by          TEXT NOT NULL DEFAULT '',
	signal      TEXT NOT NULL DEFAULT '',
	result      TEXT NOT NULL,
	detail      TEXT NOT NULL DEFAULT '',
	at          TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS proc_stops_at ON proc_stops(at);

-- 忽略孤儿进程（R-209）：只对孤儿；按设备 + “进程号@启动时间”认，进程号被复用后的新进程不受影响。
-- 行本身就是留痕：谁、何时忽略（by / at），谁、何时取消（undone_by / undone_at）；进程退出后 ended_at 记下失效时间
CREATE TABLE IF NOT EXISTS proc_ignores (
	id          TEXT PRIMARY KEY,
	device_id   TEXT NOT NULL,
	device_name TEXT NOT NULL DEFAULT '',
	proc_id     TEXT NOT NULL,
	pid         INTEGER NOT NULL,
	cmd         TEXT NOT NULL DEFAULT '',
	project     TEXT NOT NULL DEFAULT '',
	session_key TEXT NOT NULL DEFAULT '',
	by          TEXT NOT NULL DEFAULT '',
	at          TEXT NOT NULL,
	undone_by   TEXT NOT NULL DEFAULT '',
	undone_at   TEXT,
	ended_at    TEXT
);
CREATE INDEX IF NOT EXISTS proc_ignores_live ON proc_ignores(device_id, proc_id) WHERE undone_at IS NULL AND ended_at IS NULL;

-- 提交（R-156）：daemon 读本地 git log 上报的、标题写了需求编号或是 revert 的提交，以及 Agent 手动关联 / 上报 CI 的提交。
-- 纸鸢只记录与展示，不改仓库、不触发 CI。reqs 是关联的需求编号，写成 ",R-12,R-13,"；reverts 是它回滚的提交哈希；
-- source：git 本地采集 / agent 手动。ci_*：CI 结果（只读，来自 Agent 上报或 GitHub 只读接口），ci_at 是最后一次查询或上报的时间
CREATE TABLE IF NOT EXISTS commits (
	project_id   TEXT NOT NULL,
	sha          TEXT NOT NULL,
	subject      TEXT NOT NULL DEFAULT '',
	committed_at TEXT NOT NULL DEFAULT '',
	reverts      TEXT NOT NULL DEFAULT '',
	reqs         TEXT NOT NULL DEFAULT '',
	source       TEXT NOT NULL DEFAULT 'git',
	session_key  TEXT NOT NULL DEFAULT '',
	ci_state     TEXT NOT NULL DEFAULT '',
	ci_url       TEXT NOT NULL DEFAULT '',
	ci_source    TEXT NOT NULL DEFAULT '',
	ci_at        TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	PRIMARY KEY (project_id, sha)
);
