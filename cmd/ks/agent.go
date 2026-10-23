package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// stringList 是可重复的字符串参数。
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// parseAnywhere 允许参数与位置参数任意交错（Go 的 flag 包遇到第一个位置参数就停止解析）。
func parseAnywhere(fs *flag.FlagSet, args []string) error {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	return fs.Parse(append([]string{"--"}, pos...))
}

// cliSession 推断 CLI 调用所在会话：显式参数优先，其次 Claude 注入的环境变量。
func cliSession(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if sid := os.Getenv("CLAUDE_CODE_SESSION_ID"); sid != "" {
		return protocol.SessionKey(protocol.ToolClaude, sid)
	}
	return ""
}

func agentCall(method, path string, body any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out any
	if err := client.New(20*time.Second).Do(ctx, method, path, body, &out); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return nil
}

// attachFiles 经本机 daemon 上传文件，把附件 ID 写入 body["files"]。
func attachFiles(body map[string]any, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c := client.New(3 * time.Minute)
	var ids []string
	for _, p := range paths {
		var ref struct {
			ID string `json:"id"`
		}
		if err := c.Upload(ctx, "/agent/files", p, &ref); err != nil {
			return fmt.Errorf("上传 %s 失败：%w", p, err)
		}
		ids = append(ids, ref.ID)
	}
	body["files"] = ids
	return nil
}

func runReq(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法：ks req list|get|create|status|evidence ...")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("req "+sub, flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	all := fs.Bool("all", false, "list：列出所有项目")
	title := fs.String("title", "", "create：标题")
	detail := fs.String("detail", "", "create：细节")
	acceptance := fs.String("acceptance", "", "create：验收标准")
	status := fs.String("status", "", "create：初始状态")
	priority := fs.String("priority", "", "create：优先级 P0-P3")
	kind := fs.String("kind", "manual", "evidence：证据类型")
	link := fs.String("link", "", "evidence：链接")
	var images, videos stringList
	fs.Var(&images, "image", "create / evidence：附图路径，可重复")
	fs.Var(&videos, "video", "evidence：录屏路径（MP4/WebM），可重复；只在用户要求时附加")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	key := cliSession(*session)
	switch sub {
	case "list":
		q := url.Values{}
		if key != "" {
			q.Set("session_key", key)
		}
		if *all {
			q.Set("all", "true")
		}
		return agentCall("GET", "/agent/requirements?"+q.Encode(), nil)
	case "get":
		if fs.NArg() < 1 {
			return fmt.Errorf("用法：ks req get <R-编号>")
		}
		return agentCall("GET", "/agent/requirements/"+url.PathEscape(fs.Arg(0)), nil)
	case "create":
		if *title == "" {
			return fmt.Errorf("用法：ks req create --title <标题> [--detail ...] [--acceptance ...]")
		}
		body := map[string]any{"title": *title, "detail": *detail, "acceptance": *acceptance, "status": *status, "priority": *priority, "session_key": key}
		if err := attachFiles(body, images); err != nil {
			return err
		}
		return agentCall("POST", "/agent/requirements", compact(body))
	case "status":
		if fs.NArg() < 2 {
			return fmt.Errorf("用法：ks req status <R-编号> <状态>")
		}
		return agentCall("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/status", compact(map[string]any{"status": fs.Arg(1), "session_key": key}))
	case "evidence":
		if fs.NArg() < 2 {
			return fmt.Errorf("用法：ks req evidence <R-编号> <摘要> [--kind test] [--link ...] [--image 截图.png] [--video 录屏.webm]")
		}
		body := map[string]any{"kind": *kind, "summary": fs.Arg(1), "link": *link, "session_key": key}
		if err := attachFiles(body, append(images, videos...)); err != nil {
			return err
		}
		return agentCall("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/evidence", compact(body))
	case "link":
		if fs.NArg() < 1 || key == "" {
			return fmt.Errorf("用法：ks req link <R-编号> [--session tool:id]")
		}
		return agentCall("POST", "/agent/sessions/"+url.PathEscape(key)+"/link", map[string]any{"requirement": fs.Arg(0)})
	default:
		return fmt.Errorf("未知的 req 子命令：%s", sub)
	}
}

func runDecision(args []string) error {
	if len(args) < 1 || args[0] != "raise" {
		return fmt.Errorf("用法：ks decision raise --question <问题> [--option A --option B] [--recommend ...] [--req R-1]")
	}
	fs := flag.NewFlagSet("decision raise", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	question := fs.String("question", "", "问题")
	ctxText := fs.String("context", "", "背景")
	rec := fs.String("recommend", "", "推荐")
	req := fs.String("req", "", "关联需求")
	var opts, optImgs stringList
	fs.Var(&opts, "option", "选项，可重复")
	fs.Var(&optImgs, "option-image", "选项的效果图路径，按 --option 的顺序一一对应，可重复")
	approval := fs.Bool("approval", false, "请用户批准（选项固定为批准 / 拒绝，两种都可以附言）")
	var imgs stringList
	fs.Var(&imgs, "image", "问题附带的图片路径（例如待批准的方案图），可重复")
	if err := parseAnywhere(fs, args[1:]); err != nil {
		return err
	}
	if *question == "" {
		return fmt.Errorf("--question 不能为空")
	}
	body := map[string]any{"question": *question, "context": *ctxText, "recommendation": *rec, "requirement": *req, "session_key": cliSession(*session)}
	if len(opts) > 0 {
		body["options"] = []string(opts)
	}
	if *approval {
		body["approval"] = true
	}
	if len(imgs) > 0 {
		tmp := map[string]any{}
		if err := attachFiles(tmp, imgs); err != nil {
			return err
		}
		body["images"] = tmp["files"]
	}
	if len(optImgs) > 0 {
		var tmp map[string]any = map[string]any{}
		if err := attachFiles(tmp, optImgs); err != nil {
			return err
		}
		body["option_images"] = tmp["files"]
	}
	return agentCall("POST", "/agent/decisions", compact(body))
}

func compact(m map[string]any) map[string]any {
	for k, v := range m {
		if s, ok := v.(string); ok && s == "" {
			delete(m, k)
		}
	}
	return m
}

// runWait：ks wait——等用户在 Kitestring 上的答复、修改意见或留言，收到后打印出来并退出。
// Claude Code：后台运行，默认最长 1 小时（--timeout 可调，最长 12h），工具超时设得比它长。
// Codex：前台运行（它没有后台任务），默认、也最多等 30 分钟，超时后收尾结束本轮（R-74）。
func runWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id）；默认自动识别当前对话")
	timeout := fs.Duration("timeout", 0, "最长等待时间，如 30m、2h。不填：Claude Code 1 小时（最长 12h），Codex 30 分钟（也是上限）")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	q := url.Values{}
	if *timeout > 0 {
		q.Set("timeout", timeout.String())
	}
	q.Set("ppid", strconv.Itoa(os.Getppid()))
	if *session != "" {
		q.Set("session_key", *session)
	}
	fmt.Fprintln(os.Stderr, "[Kitestring] 正在等用户在 Kitestring 上答复、验收或留言；命令运行期间请一直等着，不要中断。")
	// daemon 重启（例如升级）会断开连接：在截止时间内重连继续等，而不是报错把 Agent 白白唤醒。
	ctx := signalContext()
	limit := daemon.MaxWait
	if *timeout > 0 {
		limit = min(*timeout, daemon.MaxWait)
	}
	deadline := time.Now().Add(limit)
	var res daemon.WaitResult
	for {
		left := time.Until(deadline)
		if left <= 0 {
			res.Reason = "timeout"
			break
		}
		if *timeout > 0 {
			q.Set("timeout", left.Round(time.Second).String())
		}
		err := client.New(left+time.Minute).Do(ctx, "GET", "/wait?"+q.Encode(), nil, &res)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			res.Reason = "canceled"
			break
		}
		time.Sleep(3 * time.Second)
	}
	waited := res.Timeout
	if d, err := time.ParseDuration(waited); err == nil {
		waited = humanDuration(d)
	} else if *timeout > 0 {
		waited = humanDuration(*timeout)
	}
	codex := strings.HasPrefix(res.Session, protocol.ToolCodex+":")
	switch res.Reason {
	case "delivered":
		fmt.Print(res.Text)
	case "timeout":
		if codex {
			fmt.Printf("[Kitestring] 等了 %s，用户没有新的答复。请结束本轮：用一两句话说明做到哪、在等用户什么；不要再运行 ks wait 续等。用户在 Kitestring 上答复后，会在下次对话时送达给你。\n", waited)
		} else {
			fmt.Printf("[Kitestring] 等了 %s，用户没有新的答复。可以结束本轮；用户回来后会在对话里继续。\n", waited)
		}
	case "replaced":
		fmt.Println("[Kitestring] 同一对话有新的 ks wait 接替等待，本次结束。")
	case "canceled":
		fmt.Println("[Kitestring] 等待被取消。")
	default:
		if res.Error != "" {
			return fmt.Errorf("%s", res.Error)
		}
	}
	return nil
}

// humanDuration：30m0s → 30 分钟，1h30m0s → 1 小时 30 分钟。
func humanDuration(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h > 0 && m > 0:
		return fmt.Sprintf("%d 小时 %d 分钟", h, m)
	case h > 0:
		return fmt.Sprintf("%d 小时", h)
	default:
		return fmt.Sprintf("%d 分钟", max(m, 1))
	}
}

// runPlan：计划操作。create 从 JSON 文件读计划结构（与 MCP plan_create 相同：title、goal、stages[{title, requirements[{ref | title, detail, acceptance, priority, kind}]}]）。
func runPlan(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法：ks plan create --file plan.json | get <P-编号> | submit <P-编号> <阶段序号> <小结>")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("plan "+sub, flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	file := fs.String("file", "", "create：计划结构 JSON 文件，- 表示从标准输入读")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	key := cliSession(*session)
	switch sub {
	case "create":
		if *file == "" {
			return fmt.Errorf(`用法：ks plan create --file plan.json（{"title":…,"goal":…,"stages":[{"title":…,"requirements":[{"ref":"R-12"},{"title":…}]}]}）`)
		}
		var raw []byte
		var err error
		if *file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
		body := map[string]any{}
		if err := json.Unmarshal(raw, &body); err != nil {
			return fmt.Errorf("计划 JSON 格式不对：%w", err)
		}
		if key != "" {
			body["session_key"] = key
		}
		return agentCall("POST", "/agent/plans", body)
	case "get":
		if fs.NArg() < 1 {
			return fmt.Errorf("用法：ks plan get <P-编号>")
		}
		return agentCall("GET", "/agent/plans/"+url.PathEscape(fs.Arg(0)), nil)
	case "submit":
		if fs.NArg() < 3 {
			return fmt.Errorf("用法：ks plan submit <P-编号> <阶段序号> <阶段小结>")
		}
		seq, err := strconv.Atoi(fs.Arg(1))
		if err != nil {
			return fmt.Errorf("阶段序号应为数字：%s", fs.Arg(1))
		}
		return agentCall("POST", fmt.Sprintf("/agent/plans/%s/stages/%d/submit", url.PathEscape(fs.Arg(0)), seq), compact(map[string]any{"summary": fs.Arg(2), "session_key": key}))
	default:
		return fmt.Errorf("未知的 plan 子命令：%s", sub)
	}
}
