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

-- 工作台泳道时间线（R-205）：每个会话、每个子任务每分钟的工具调用次数（PreToolUse）。
-- agent_id 为空是主会话那条。新事件只把它所在那一分钟加一，算过的分钟不再重算（daemon 断线重放的迟到事件补进它真实的那一分钟）。
-- minute 是 Unix 分钟（秒 / 60）。保留 30 天；lane_hours 同时按小时累计（近 1 周档直接读它），30 天前的只剩小时级。
-- token 用量（R-206）按模型分行，放在旁表 lane_tokens / lane_token_hours。
CREATE TABLE IF NOT EXISTS lane_minutes (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	minute      INTEGER NOT NULL,
	calls       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_key, agent_id, minute)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS lane_minutes_minute ON lane_minutes(minute);
CREATE TABLE IF NOT EXISTS lane_hours (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	minute      INTEGER NOT NULL, -- 整点的 Unix 分钟
	calls       INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_key, agent_id, minute)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS lane_hours_minute ON lane_hours(minute);

-- 时间线上的时段（R-205）：kind = tool 长命令（工具开始到结束超过 20 秒），wait 用 ks wait 待命。
-- ref 是 tool_use_id 或 KsWait 事件的 id；end_ms 为空表示还没结束（待命最长画到 until_ms）。时间都是 Unix 毫秒。保留 30 天。
CREATE TABLE IF NOT EXISTS lane_spans (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL,
	ref         TEXT NOT NULL,
	label       TEXT NOT NULL DEFAULT '',
	start_ms    INTEGER NOT NULL,
	end_ms      INTEGER,
	until_ms    INTEGER,
	PRIMARY KEY (session_key, kind, ref)
);
CREATE INDEX IF NOT EXISTS lane_spans_start ON lane_spans(start_ms);

-- 子任务（R-205）：每个子代理一行，结束后也留着（时间线上留到滑出窗口），保留 30 天。
CREATE TABLE IF NOT EXISTS lane_agents (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL,
	label       TEXT NOT NULL DEFAULT '',
	agent_type  TEXT NOT NULL DEFAULT '',
	started_ms  INTEGER NOT NULL,
	ended_ms    INTEGER,
	PRIMARY KEY (session_key, agent_id)
);

-- token 用量（R-206）：各设备 daemon 从对话记录读出、按分钟汇总上传的数字（不含对话内容），每个会话 / 子任务、每个模型、每分钟一行。
-- inp 没命中缓存的输入，cw 写缓存（cw1h 是其中 1 小时档的，其余 5 分钟档），cr 读缓存，out 输出，n 回复次数。新算 = inp + cw + out；命中率 = cr / (cr + inp + cw)。
-- daemon 发的是累计值（绝对值），这里直接覆盖，重发不重复计；lane_token_hours 由同一小时的分钟重新求和。保留与压缩和 lane_minutes 一样。
CREATE TABLE IF NOT EXISTS lane_tokens (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	model       TEXT NOT NULL DEFAULT '',
	minute      INTEGER NOT NULL,
	inp         INTEGER NOT NULL DEFAULT 0,
	cw          INTEGER NOT NULL DEFAULT 0,
	cw1h        INTEGER NOT NULL DEFAULT 0,
	cr          INTEGER NOT NULL DEFAULT 0,
	out         INTEGER NOT NULL DEFAULT 0,
	n           INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_key, agent_id, minute, model)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS lane_tokens_minute ON lane_tokens(minute);
CREATE TABLE IF NOT EXISTS lane_token_hours (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	model       TEXT NOT NULL DEFAULT '',
	minute      INTEGER NOT NULL, -- 整点的 Unix 分钟
	inp         INTEGER NOT NULL DEFAULT 0,
	cw          INTEGER NOT NULL DEFAULT 0,
	cw1h        INTEGER NOT NULL DEFAULT 0,
	cr          INTEGER NOT NULL DEFAULT 0,
	out         INTEGER NOT NULL DEFAULT 0,
	n           INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_key, agent_id, minute, model)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS lane_token_hours_minute ON lane_token_hours(minute);

-- 冷启动（R-206）：单次回复写缓存超过 1.5 万 token。cause：idle 闲置 / compact 上下文压缩 / model 换模型 / start 第一次回复 / other。保留 30 天。
CREATE TABLE IF NOT EXISTS lane_colds (
	session_key TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	at_ms       INTEGER NOT NULL,
	model       TEXT NOT NULL DEFAULT '',
	tokens      INTEGER NOT NULL,
	cause       TEXT NOT NULL,
	idle_min    INTEGER NOT NULL DEFAULT 0,
	prev_model  TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (session_key, agent_id, at_ms)
);
CREATE INDEX IF NOT EXISTS lane_colds_at ON lane_colds(at_ms);

-- 后台命令（R-208，shell 泳道）：各设备 daemon 记下的 run_in_background 命令（Codex 是会话下的后台 shell），每条一行，daemon 有变化时整条覆盖。
-- ref 是 tool_use_id（Codex 是“sh-进程号@启动时间”），tid 是对应 R-162 后台任务的 ID；kind：build 构建 / 测试、deploy 部署、service 服务、poll 轮询、other。
-- status：running / ok / failed / stopped / ended（已结束，退出码未知）/ lost（结束时间也不知道）。last_line 是最后一行输出的摘要（截断），不存输出内容。
-- hot：跑飞（cpu / mem），hot_ms 开始时刻，rss_kb 当时整棵进程树的内存。时间都是 Unix 毫秒。保留 30 天。
CREATE TABLE IF NOT EXISTS lane_shells (
	session_key TEXT NOT NULL,
	ref         TEXT NOT NULL,
	agent_id    TEXT NOT NULL DEFAULT '',
	tid         TEXT NOT NULL DEFAULT '',
	device_id   TEXT NOT NULL DEFAULT '',
	kind        TEXT NOT NULL DEFAULT 'other',
	label       TEXT NOT NULL DEFAULT '',
	cmd         TEXT NOT NULL DEFAULT '',
	start_ms    INTEGER NOT NULL,
	end_ms      INTEGER,
	status      TEXT NOT NULL,
	exit_code   INTEGER,
	last_line   TEXT NOT NULL DEFAULT '',
	hot         TEXT NOT NULL DEFAULT '',
	hot_ms      INTEGER,
	rss_kb      INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (session_key, ref)
);
CREATE INDEX IF NOT EXISTS lane_shells_start ON lane_shells(start_ms);

-- 会话交接（R-224）：换会话 / 换工具 / 换设备时，旧会话用 handoff_write 写结构化交接，新会话 ks role 接任时自动注入。
-- status：requested 用户点了「交接」、等旧会话写；written 已写好、等新会话接任；taken 已被接任（taken_by / taken_at）。
CREATE TABLE IF NOT EXISTS handoffs (
	id           TEXT PRIMARY KEY,
	project_id   TEXT NOT NULL,
	role         TEXT NOT NULL,
	from_session TEXT NOT NULL,
	from_title   TEXT NOT NULL DEFAULT '',
	status       TEXT NOT NULL,
	doing        TEXT NOT NULL DEFAULT '',
	waiting      TEXT NOT NULL DEFAULT '',
	rules        TEXT NOT NULL DEFAULT '',
	next         TEXT NOT NULL DEFAULT '',
	notes        TEXT NOT NULL DEFAULT '',
	requested_at TEXT NOT NULL DEFAULT '',
	written_at   TEXT NOT NULL DEFAULT '',
	taken_by     TEXT NOT NULL DEFAULT '',
	taken_title  TEXT NOT NULL DEFAULT '',
	taken_at     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS handoffs_project_role ON handoffs(project_id, role);
CREATE INDEX IF NOT EXISTS handoffs_from ON handoffs(from_session);

-- 暂时离开（R-210）：你点「暂时离开」时记一行，起点就是这一刻；只能由你点「结束离开」结束（ended_at）。
-- expect_minutes 是预估离开多久（0 = 不确定），只用来标「已超过预估」；scope 是给主助理的授权范围（none 不代答 / low / medium），
-- 开离开时按它给各项目的主助理开授权窗口（grants.away_id 指回这里）；hold_push = 这次离开期间连推送类也先攒着（拿不准的仍推）。
-- calm_since：最近一次「能处理的都处理完」开始成立的时间；settled_at：最近一次生成阶段性报告的时间；
CREATE TABLE IF NOT EXISTS aways (
	id             TEXT PRIMARY KEY,
	seq            INTEGER NOT NULL UNIQUE, -- A-<seq>
	started_at     TEXT NOT NULL,
	expect_minutes INTEGER NOT NULL DEFAULT 0,
	scope          TEXT NOT NULL DEFAULT 'none',
	hold_push      INTEGER NOT NULL DEFAULT 0,
	ended_at       TEXT NOT NULL DEFAULT '',
	calm_since     TEXT NOT NULL DEFAULT '',
	settled_at     TEXT NOT NULL DEFAULT '',
	worked         INTEGER NOT NULL DEFAULT 0 -- 上次阶段性报告之后有没有发生过事（会话在跑、有推送 / 处理）
);

-- 离开期间发生的事（R-210），给时段报告用：
-- tier = push 推了 / hold 勿扰先攒着 / merge 合并进报告 / skip 不推（已可代答、会话空闲）/ user 你在某台设备上处理了 / settled 阶段性报告；
-- event 是通知事件或你的操作，ref 是关联编号（D-x / R-x / P-x），device 是你处理时用的设备（如 iPhone）；
-- normal = 不在离开时这条会不会推
CREATE TABLE IF NOT EXISTS away_events (
	id         TEXT PRIMARY KEY,
	away_id    TEXT NOT NULL REFERENCES aways(id) ON DELETE CASCADE,
	at         TEXT NOT NULL,
	tier       TEXT NOT NULL,
	event      TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL DEFAULT '',
	ref        TEXT NOT NULL DEFAULT '',
	title      TEXT NOT NULL DEFAULT '',
	body       TEXT NOT NULL DEFAULT '',
	reason     TEXT NOT NULL DEFAULT '',
	device     TEXT NOT NULL DEFAULT '',
	normal     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS away_events_away ON away_events(away_id, at);

-- 订阅需求（R-249 P4）：会话 watch 某条需求，有变动时收到一句话提醒。
-- 实际订阅者 = 这里 off = 0 的行 ∪ 关联这条需求（requirement_sessions）且没结束的会话（隐式订阅，不写行），再去掉 off = 1 的行：
-- 显式退订优先于隐式订阅。mode = all 两档都收；action 只收「要动手」那一档。
CREATE TABLE IF NOT EXISTS req_watches (
	requirement_id TEXT NOT NULL REFERENCES requirements(id) ON DELETE CASCADE,
	session_key    TEXT NOT NULL,
	mode           TEXT NOT NULL DEFAULT 'all', -- all | action
	off            INTEGER NOT NULL DEFAULT 0,  -- 1 = 退订（含退掉因关联而来的隐式订阅）
	created_at     TEXT NOT NULL,
	PRIMARY KEY (requirement_id, session_key)
);
CREATE INDEX IF NOT EXISTS req_watches_session ON req_watches(session_key);

-- 当前安排（R-245）：用户口头交代的临时安排（如「额度紧，做完这几条就停」「这次可以 push」），落成项目上的一条短记录，
-- 接任的会话从这里读到，不靠交接转述。by_session 为空表示用户在网页上加的；状态（生效中 / 已到期 / 已撤销）按 expires_at、revoked_at 推算，不删除，留痕
CREATE TABLE IF NOT EXISTS arrangements (
	id         TEXT PRIMARY KEY,
	seq        INTEGER NOT NULL UNIQUE, -- N-<seq>
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	text       TEXT NOT NULL,
	by_session TEXT NOT NULL DEFAULT '',
	by_title   TEXT NOT NULL DEFAULT '',
	by_role    TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	expires_at TEXT NOT NULL DEFAULT '',
	revoked_at TEXT NOT NULL DEFAULT '',
	revoked_by TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS arrangements_project ON arrangements(project_id, created_at);

-- 每轮回复（R-273）：会话每轮结束（Stop）时的最后一条回复，最长 2 万字，按 Stop 事件存一行；会话详情页按轮显示。
-- 只从上线起记，之前的轮用 Stop 事件里带的摘要用回复兜底。清理跟 events 一致（目前不清理）。
CREATE TABLE IF NOT EXISTS turn_replies (
	event_id    TEXT PRIMARY KEY,
	session_key TEXT NOT NULL,
	turn_id     TEXT NOT NULL DEFAULT '',
	at          TEXT NOT NULL,
	text        TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS turn_replies_session ON turn_replies(session_key, at);

-- 分享（R-281）：把计划、阶段、需求、会话挂到一个分享上，给熟人 / 团队成员免登录只读访问。
-- 状态（生效中 / 已失效 / 已撤回）按 expires_at、revoked_at 推算；expires_at 为空表示永久。密码可选，所有链接共用（bcrypt）
CREATE TABLE IF NOT EXISTS shares (
	id            TEXT PRIMARY KEY,
	name          TEXT NOT NULL,
	project_id    TEXT REFERENCES projects(id) ON DELETE SET NULL,
	password_hash TEXT NOT NULL DEFAULT '',
	expires_at    TEXT NOT NULL DEFAULT '',
	revoked_at    TEXT NOT NULL DEFAULT '',
	created_at    TEXT NOT NULL,
	updated_at    TEXT NOT NULL
);

-- 分享的挂载项：kind = plan | stage | req | session，ref 是计划 / 阶段 / 需求的 ID 或会话键。
-- auto：包含以后新增的（计划后来新增的阶段和需求、需求后来关联的会话）；excluded：被单独取消，上级的 auto 不会把它带回来
CREATE TABLE IF NOT EXISTS share_items (
	share_id TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	kind     TEXT NOT NULL,
	ref      TEXT NOT NULL,
	auto     INTEGER NOT NULL DEFAULT 0,
	excluded INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (share_id, kind, ref)
);

-- 访客链接：每个访客一条，带名字；token 要能随时复制，存原文。撤回后下一次访问即拒绝
CREATE TABLE IF NOT EXISTS share_links (
	id            TEXT PRIMARY KEY,
	share_id      TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	name          TEXT NOT NULL,
	token         TEXT NOT NULL UNIQUE,
	created_at    TEXT NOT NULL,
	revoked_at    TEXT NOT NULL DEFAULT '',
	last_visit_at TEXT NOT NULL DEFAULT '',
	visits        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS share_links_share ON share_links(share_id);

-- 访客留言（R-281）：访客在分享页上对需求、决策、证据、阶段的留言。先进待收队列（pending），
-- 所有者原样或改写放入（placed，写成需求留言 decision_id）或忽略（ignored）。访客只能写这一张表
CREATE TABLE IF NOT EXISTS share_comments (
	id             TEXT PRIMARY KEY,
	share_id       TEXT NOT NULL REFERENCES shares(id) ON DELETE CASCADE,
	link_id        TEXT NOT NULL DEFAULT '',
	guest_name     TEXT NOT NULL,
	target_kind    TEXT NOT NULL, -- req | dec | evidence | stage
	target_ref     TEXT NOT NULL, -- 需求 ID、决策 ID、证据 ID、阶段 ID
	requirement_id TEXT NOT NULL DEFAULT '', -- 留言所属的需求；挂在阶段上时为空
	plan_id        TEXT NOT NULL DEFAULT '',
	stage_seq      INTEGER NOT NULL DEFAULT 0,
	text           TEXT NOT NULL,
	status         TEXT NOT NULL DEFAULT 'pending',
	decision_id    TEXT NOT NULL DEFAULT '',
	created_at     TEXT NOT NULL,
	handled_at     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS share_comments_status ON share_comments(status, created_at);
CREATE INDEX IF NOT EXISTS share_comments_link ON share_comments(link_id, created_at);
