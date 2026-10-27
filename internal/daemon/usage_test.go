package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

const secret = "机密对话内容-SECRET-42"

var usageT0 = time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)

func at(min int) string {
	return usageT0.Add(time.Duration(min) * time.Minute).Format("2006-01-02T15:04:05.000Z")
}

// claudeMsg 生成一条 assistant 记录（内容里带机密文字，验证上传的只有数字）。
func claudeMsg(min int, id, model string, in, cw5, cw1h, cr, out int64) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"isSidechain":false,"requestId":"req_%s","message":{"id":%q,"model":%q,"role":"assistant","content":[{"type":"text","text":%q}],"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d,"cache_creation":{"ephemeral_5m_input_tokens":%d,"ephemeral_1h_input_tokens":%d}}}}`+"\n",
		at(min), id, id, model, secret, in, cw5+cw1h, cr, out, cw5, cw1h)
}

func userMsg(min int) string {
	return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"role":"user","content":[{"type":"tool_result","content":"\"usage\" %s"}]}}`+"\n", at(min), secret)
}

func compactLine(min int) string {
	return fmt.Sprintf(`{"type":"system","subtype":"compact_boundary","timestamp":%q,"content":"Conversation compacted"}`+"\n", at(min))
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func newUsage(t *testing.T, dir string) (*queue, *usageScanner) {
	t.Helper()
	q, err := openQueue(filepath.Join(dir, "daemon.db"))
	if err != nil {
		t.Fatal(err)
	}
	u, err := openUsage(q.db)
	if err != nil {
		t.Fatal(err)
	}
	return q, u
}

// 插件直接上报的用量（DSH）：按分钟累加、版本号 +1、sent 置 0，等下一批发给 Hub。
func TestUsageRecordFromPlugin(t *testing.T) {
	q, u := newUsage(t, t.TempDir())
	at := usageT0.Add(5 * time.Minute)
	if err := u.record("dsh:s1", "deepseek-flash", at, 100, 10, 20, 30, 1); err != nil {
		t.Fatal(err)
	}
	if err := u.record("dsh:s1", "deepseek-flash", at, 50, 0, 0, 10, 1); err != nil {
		t.Fatal(err)
	}
	var inp, cw, cr, out, n, ver, sent int64
	row := q.db.QueryRow(`SELECT inp, cw, cr, out, n, ver, sent FROM usage_minutes WHERE session_key = ? AND agent_id = '' AND minute = ?`, "dsh:s1", at.Unix()/60)
	if err := row.Scan(&inp, &cw, &cr, &out, &n, &ver, &sent); err != nil {
		t.Fatal(err)
	}
	if inp != 150 || cw != 10 || cr != 20 || out != 40 || n != 2 || ver != 2 || sent != 0 {
		t.Fatalf("累加结果不对：inp=%d cw=%d cr=%d out=%d n=%d ver=%d sent=%d", inp, cw, cr, out, n, ver, sent)
	}
	env, ok := u.pending()
	if !ok || len(env.Usage) != 1 || env.Usage[0].Input != 150 || env.Usage[0].Requests != 2 {
		t.Fatalf("pending 应带上这条用量：%+v ok=%v", env.Usage, ok)
	}
}

type tot struct{ in, cw, cw1h, cr, out, n int64 }

func totals(t *testing.T, u *usageScanner, key, agent string) tot {
	t.Helper()
	var r tot
	if err := u.db.QueryRow(`SELECT COALESCE(SUM(inp),0), COALESCE(SUM(cw),0), COALESCE(SUM(cw1h),0), COALESCE(SUM(cr),0), COALESCE(SUM(out),0), COALESCE(SUM(n),0)
		FROM usage_minutes WHERE session_key = ? AND agent_id = ?`, key, agent).Scan(&r.in, &r.cw, &r.cw1h, &r.cr, &r.out, &r.n); err != nil {
		t.Fatal(err)
	}
	return r
}

func colds(t *testing.T, u *usageScanner) []protocol.ColdStart {
	t.Helper()
	rows, err := u.db.Query(`SELECT session_key, agent_id, at_ms, model, tokens, cause, idle_min, prev_model FROM usage_colds ORDER BY at_ms`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []protocol.ColdStart
	for rows.Next() {
		var c protocol.ColdStart
		if err := rows.Scan(&c.Key, &c.Agent, &c.At, &c.Model, &c.Tokens, &c.Cause, &c.IdleMin, &c.PrevModel); err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

// 增量读、按回复编号去重、子任务记录、写不完整的最后一行、重启后续读都不重复计。
func TestUsageIncrementalDedupRestart(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "proj", "sess1.jsonl")
	sub := filepath.Join(dir, "proj", "sess1", "subagents", "agent-abc123.jsonl")
	key := "claude:sess1"
	// 同一条回复按内容块出现两次（usage 相同），只算一次
	appendFile(t, main, claudeMsg(0, "msg_1", "claude-opus-5-5", 3, 0, 20000, 0, 100)+claudeMsg(0, "msg_1", "claude-opus-5-5", 3, 0, 20000, 0, 100)+userMsg(1)+claudeMsg(1, "msg_2", "claude-opus-5-5", 2, 0, 500, 20000, 50))
	appendFile(t, sub, claudeMsg(2, "msg_s1", "claude-sonnet-5", 4, 16000, 0, 0, 10))

	q, u := newUsage(t, dir)
	u.track(key, main)
	u.track(key, sub) // 子任务路径不直接登记，由主会话目录推出来
	u.scan()
	if got := totals(t, u, key, ""); got != (tot{in: 5, cw: 20500, cw1h: 20500, cr: 20000, out: 150, n: 2}) {
		t.Fatalf("主会话 = %+v", got)
	}
	if got := totals(t, u, key, "abc123"); got != (tot{in: 4, cw: 16000, cr: 0, out: 10, n: 1}) {
		t.Fatalf("子任务 = %+v", got)
	}
	// 没有新增：再读一轮不变
	u.scan()
	if got := totals(t, u, key, ""); got.n != 2 || got.out != 150 {
		t.Fatalf("没有新增却变了：%+v", got)
	}
	// 重复行跨两次读取（第二次读到同一条回复的另一个内容块），还写了一半的最后一行
	half := claudeMsg(5, "msg_3", "claude-opus-5-5", 1, 0, 0, 21000, 7)
	appendFile(t, main, claudeMsg(1, "msg_2", "claude-opus-5-5", 2, 0, 500, 20000, 50)+half[:40])
	u.scan()
	if got := totals(t, u, key, ""); got.n != 2 || got.out != 150 {
		t.Fatalf("跨读取的重复行或半行被计入：%+v", got)
	}
	// daemon 重启：从记下的位置续读
	q.close()
	q, u = newUsage(t, dir)
	defer q.close()
	appendFile(t, main, half[40:])
	u.scan()
	if got := totals(t, u, key, ""); got != (tot{in: 6, cw: 20500, cw1h: 20500, cr: 41000, out: 157, n: 3}) {
		t.Fatalf("重启后 = %+v", got)
	}
	u.scan()
	if got := totals(t, u, key, "abc123"); got.n != 1 {
		t.Fatalf("重启后子任务重复计：%+v", got)
	}
}

// 冷启动：第一次回复、闲置超过档位、上下文压缩、换模型各记一次起因；不超过阈值的、前面照样命中缓存的不记。
func TestUsageColdStartCauses(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "p", "s.jsonl")
	appendFile(t, main,
		claudeMsg(0, "m1", "claude-opus-5-5", 3, 30000, 0, 0, 10)+ // 第一次
			claudeMsg(2, "m2", "claude-opus-5-5", 3, 400, 0, 30000, 10)+ // 热的，不算
			claudeMsg(3, "m2b", "claude-opus-5-5", 3, 20000, 0, 30400, 10)+ // 新加一大段内容、前面照样命中：不算
			claudeMsg(20, "m3", "claude-opus-5-5", 3, 31000, 0, 0, 10)+ // 闲置 17 分钟 > 5 分钟档
			compactLine(21)+
			claudeMsg(22, "m4", "claude-opus-5-5", 3, 0, 9000, 0, 10)+ // 压缩后但不到阈值：不算，也清掉压缩标记
			compactLine(23)+
			claudeMsg(24, "m5", "claude-opus-5-5", 3, 0, 18000, 0, 10)+ // 压缩
			claudeMsg(30, "m6", "claude-sonnet-5", 3, 0, 40000, 0, 10)+ // 换模型（1 小时档内）
			claudeMsg(80, "m7", "claude-sonnet-5", 3, 0, 40000, 0, 10)) // 1 小时档内闲置 50 分钟：看不出起因
	q, u := newUsage(t, dir)
	defer q.close()
	u.track("claude:s", main)
	u.scan()
	got := colds(t, u)
	want := []struct {
		cause string
		idle  int
	}{{protocol.ColdFirst, 0}, {protocol.ColdIdle, 17}, {protocol.ColdCompact, 2}, {protocol.ColdModel, 6}, {protocol.ColdOther, 50}}
	if len(got) != len(want) {
		t.Fatalf("冷启动 %d 次：%+v", len(got), got)
	}
	for i, w := range want {
		if got[i].Cause != w.cause || got[i].IdleMin != w.idle {
			t.Fatalf("第 %d 次 = %+v，想要 %+v", i, got[i], w)
		}
	}
	if got[3].PrevModel != "claude-opus-5-5" || got[3].Model != "claude-sonnet-5" {
		t.Fatalf("换模型没记前后模型：%+v", got[3])
	}
}

// Codex：按 response_id 去重（有 token_usage_record 时），否则按 token_count 的累计总数；input 含命中缓存的部分。
func TestUsageCodex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout-x.jsonl")
	tc := func(min int, total, in, cached, out int64) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":%d},"last_token_usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d}}}}`+"\n", at(min), total, in, cached, out)
	}
	appendFile(t, path, fmt.Sprintf(`{"timestamp":%q,"type":"turn_context","payload":{"model":"gpt-6-astra","cwd":%q}}`+"\n", at(0), secret)+
		tc(0, 20285, 20112, 0, 173)+tc(0, 20285, 20112, 0, 173)+ // 重复通知
		tc(1, 41612, 21288, 19968, 39))
	q, u := newUsage(t, dir)
	defer q.close()
	u.track("codex:x", path)
	u.scan()
	if got := totals(t, u, "codex:x", ""); got != (tot{in: 20112 + 1320, cr: 19968, out: 212, n: 2}) {
		t.Fatalf("token_count = %+v", got)
	}
	// 新格式：token_usage_record 按 response_id 去重，之后不再看 token_count
	rec := func(min int, id string, in, cached, out int64) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"token_usage_record","payload":{"response_id":%q,"usage":{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":0,"output_tokens":%d}}}`+"\n", at(min), id, in, cached, out)
	}
	appendFile(t, path, rec(2, "resp_a", 30000, 29000, 10)+rec(2, "resp_a", 30000, 29000, 10)+tc(2, 71622, 30000, 29000, 10))
	u.scan()
	if got := totals(t, u, "codex:x", ""); got.n != 3 || got.cr != 19968+29000 {
		t.Fatalf("token_usage_record = %+v", got)
	}
	var model string
	_ = u.db.QueryRow(`SELECT model FROM usage_minutes WHERE session_key = 'codex:x' LIMIT 1`).Scan(&model)
	if model != "gpt-6-astra" {
		t.Fatalf("模型 = %q", model)
	}
	if c := colds(t, u); len(c) != 1 || c[0].Cause != protocol.ColdFirst || c[0].Tokens != 20112 {
		t.Fatalf("Codex 冷启动 = %+v", c)
	}
}

// 上传的只有数字：消息里没有对话内容；发出后 Hub 回了批次号才算发过，同一分钟又有新增时重发累计值。
func TestUsageUploadOnlyNumbers(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "p", "s.jsonl")
	appendFile(t, main, userMsg(0)+claudeMsg(0, "m1", "claude-opus-5-5", 3, 30000, 0, 0, 10)+compactLine(1)+claudeMsg(1, "m2", "claude-opus-5-5", 3, 20000, 0, 100, 10))
	appendFile(t, filepath.Join(dir, "p", "s", "subagents", "agent-a1.jsonl"), claudeMsg(1, "s1", "claude-sonnet-5", 1, 0, 0, 0, 1))
	q, u := newUsage(t, dir)
	defer q.close()
	u.track("claude:s", main)
	u.scan()
	env, ok := u.pending()
	if !ok || len(env.Usage) != 3 || len(env.Colds) != 2 || env.Batch == "" {
		t.Fatalf("待发 = %+v", env)
	}
	b, _ := json.Marshal(env)
	for _, bad := range []string{"SECRET", "机密", "Conversation compacted", "m1", "req_", "/p/"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("上传内容里出现了 %q：%s", bad, b)
		}
	}
	// 结构上也只有会话键 / 子任务编号 / 模型名 / 起因这几个字符串，其余都是数字
	allowed := map[string]bool{"Key": true, "Agent": true, "Model": true, "Cause": true, "PrevModel": true}
	for _, typ := range []reflect.Type{reflect.TypeOf(protocol.UsageMinute{}), reflect.TypeOf(protocol.ColdStart{})} {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			switch f.Type.Kind() {
			case reflect.Int, reflect.Int64:
			case reflect.String:
				if !allowed[f.Name] {
					t.Fatalf("%s.%s 是字符串，可能带出内容", typ.Name(), f.Name)
				}
			default:
				t.Fatalf("%s.%s 类型 %s", typ.Name(), f.Name, f.Type)
			}
		}
	}
	// 没回执：等待期内不重发
	if _, ok := u.pending(); ok {
		t.Fatal("没回执就又发了")
	}
	u.acked([]string{env.Batch})
	if _, ok := u.pending(); ok {
		t.Fatal("回执后还有待发")
	}
	// 同一分钟又来一条：重发这一分钟的累计值
	appendFile(t, main, claudeMsg(1, "m3", "claude-opus-5-5", 2, 0, 0, 50000, 5))
	u.scan()
	env, ok = u.pending()
	if !ok || len(env.Usage) != 1 || env.Usage[0].Requests != 2 || env.Usage[0].CacheR != 50100 || len(env.Colds) != 0 {
		t.Fatalf("再发 = %+v", env)
	}
	// 断线：没回执的批次忘掉，马上能重发同样的值
	u.reset()
	env2, ok := u.pending()
	if !ok || len(env2.Usage) != 1 || env2.Usage[0] != env.Usage[0] {
		t.Fatalf("重连后重发 = %+v", env2)
	}
}

// 读文件省资源：每轮每个文件最多读 usageChunk，剩下的下一轮接着读；大小没变不打开文件。
func TestUsageChunked(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "p", "big.jsonl")
	pad := strings.Repeat("x", 1<<20)
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString(fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":%q}}`+"\n", at(0), pad))
		b.WriteString(claudeMsg(0, fmt.Sprint("m", i), "claude-opus-5-5", 1, 0, 0, 0, 1))
	}
	appendFile(t, main, b.String())
	q, u := newUsage(t, dir)
	defer q.close()
	u.track("claude:big", main)
	u.scan()
	first := totals(t, u, "claude:big", "").n
	if first == 0 || first >= 10 {
		t.Fatalf("第一轮读了 %d 条，应只读一部分", first)
	}
	for i := 0; i < 5; i++ {
		u.scan()
	}
	if got := totals(t, u, "claude:big", "").n; got != 10 {
		t.Fatalf("读完 = %d", got)
	}
}
