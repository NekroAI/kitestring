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

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/writing"
)

// stringList 是可重复的字符串参数。
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// intList 是可重复的整数参数。
type intList []int

func (s *intList) String() string { return fmt.Sprint([]int(*s)) }
func (s *intList) Set(v string) error {
	n, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("要填数字：%s", v)
	}
	*s = append(*s, n)
	return nil
}

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
	_, err := agentCallOut(method, path, body)
	return err
}

// agentCallOut 同 agentCall，并把返回的 JSON 交给调用方（读 block_warnings 等）。
func agentCallOut(method, path string, body any) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out any
	if err := client.New(20*time.Second).Do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return out, nil
}

// attachBlocks 读 --blocks（JSON 数组，或指向 JSON 文件的路径），上传其中的本机图片，写入 body["blocks"]（R-97）。
func attachBlocks(body map[string]any, arg string) ([]any, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, nil
	}
	data := []byte(arg)
	if !strings.HasPrefix(arg, "[") && !strings.HasPrefix(arg, "{") {
		b, err := os.ReadFile(arg)
		if err != nil {
			return nil, fmt.Errorf("--blocks 既不是 JSON 也读不到文件：%w", err)
		}
		data = b
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("--blocks 的 JSON 格式不对：%w", err)
	}
	raw, ok := v.([]any)
	if !ok {
		raw = []any{v} // 只给了一个块
	}
	err := blocks.Localize(raw, map[string]string{}, func(p string) (string, error) {
		tmp := map[string]any{}
		if err := attachFiles(tmp, []string{p}); err != nil {
			return "", err
		}
		return tmp["files"].([]string)[0], nil
	})
	if err != nil {
		return nil, err
	}
	body["blocks"] = raw
	return raw, nil
}

// printBlockWarnings 输出没有保存的块及原因（其余内容已保存）。
func printBlockWarnings(out any) {
	m, _ := out.(map[string]any)
	ws, _ := m["block_warnings"].([]any)
	if len(ws) == 0 {
		return
	}
	fmt.Println("有块或改动框没有保存（其余内容已保存）：")
	for _, w := range ws {
		fmt.Println("- " + fmt.Sprint(w))
	}
}

func printImageHints(out any) {
	m, _ := out.(map[string]any)
	ws, _ := m["image_hints"].([]any)
	if len(ws) == 0 {
		return
	}
	fmt.Println("附图提示（证据已保存）：")
	for _, w := range ws {
		fmt.Println("- " + fmt.Sprint(w))
	}
}

// attachShots 处理 evidence 的附图（R-183）：--image 可以是路径，也可以是 {"path", "title", "boxes"} 的 JSON 对象；
// --images 是这种对象的 JSON 数组或 JSON 文件路径。按给的顺序（先 --image 后 --images）上传后写进 body["images"]。
func attachShots(body map[string]any, images []string, arg string) error {
	var raw []any
	for _, s := range images {
		if t := strings.TrimSpace(s); strings.HasPrefix(t, "{") {
			var v any
			if err := json.Unmarshal([]byte(t), &v); err != nil {
				return fmt.Errorf("--image 的 JSON 格式不对：%w", err)
			}
			raw = append(raw, v)
			continue
		}
		raw = append(raw, s)
	}
	if arg = strings.TrimSpace(arg); arg != "" {
		data := []byte(arg)
		if !strings.HasPrefix(arg, "[") && !strings.HasPrefix(arg, "{") {
			b, err := os.ReadFile(arg)
			if err != nil {
				return fmt.Errorf("--images 既不是 JSON 也读不到文件：%w", err)
			}
			data = b
		}
		var v any
		if err := json.Unmarshal(data, &v); err != nil {
			return fmt.Errorf("--images 的 JSON 格式不对：%w", err)
		}
		if l, ok := v.([]any); ok {
			raw = append(raw, l...)
		} else {
			raw = append(raw, v)
		}
	}
	if len(raw) == 0 {
		return nil
	}
	err := blocks.LocalizeImages(raw, map[string]string{}, func(p string) (string, error) {
		tmp := map[string]any{}
		if err := attachFiles(tmp, []string{p}); err != nil {
			return "", err
		}
		return tmp["files"].([]string)[0], nil
	})
	if err != nil {
		return err
	}
	body["images"] = raw
	return nil
}

func rawHasChecklist(raw []any) bool {
	for _, b := range raw {
		if m, ok := b.(map[string]any); ok && strings.EqualFold(fmt.Sprint(m["type"]), blocks.Checklist) {
			return true
		}
	}
	return false
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

// editBody 拼 ks req edit 的请求：只带命令行上给出的字段（给了空串表示清空），没给的不改（R-134）。
func editBody(fs *flag.FlagSet, key, reason, approval string) map[string]any {
	body := map[string]any{"session_key": key}
	names := map[string]string{"title": "title", "detail": "detail", "acceptance": "acceptance", "priority": "priority", "type": "kind"}
	fs.Visit(func(f *flag.Flag) {
		if name, ok := names[f.Name]; ok {
			body[name] = f.Value.String()
		}
	})
	if reason != "" {
		body["reason"] = reason
	}
	if approval != "" {
		body["approval"] = approval
	}
	return body
}

func runReq(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法：ks req list|get|create|edit|status|request|evidence|assign|review ...")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("req "+sub, flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	all := fs.Bool("all", false, "list：列出所有项目")
	title := fs.String("title", "", "create / edit：标题")
	detail := fs.String("detail", "", "create / edit：细节")
	acceptance := fs.String("acceptance", "", "create / edit：验收标准")
	status := fs.String("status", "", "create：初始状态")
	fromDec := fs.String("from-decision", "", "create：产生这条需求的决策编号（D-x）")
	priority := fs.String("priority", "", "create / edit：优先级 P0-P3")
	fs.String("type", "", "edit：需求类型 design（设计类）/ feature（普通）")
	approval := fs.String("approval", "", "edit：进行中之后改验收标准时，用户已批准的批准类决策编号（D-x）")
	kind := fs.String("kind", "manual", "evidence：证据类型")
	link := fs.String("link", "", "evidence：链接")
	to := fs.String("to", "", "assign：交给哪个 Dev 会话（tool:session_id），auto 由 hub 挑")
	comment := fs.String("comment", "", "assign：交代给开发的话")
	reason := fs.String("reason", "", "request：申请理由（必填）；status ready / assign 草稿：放行理由（只有 PM / Adv 能放行）；status draft：撤回原因；status shelved：搁置原因（必填）；edit：为什么改")
	reasonKind := fs.String("reason-kind", "", "status shelved：搁置原因类别 duplicate（重复）/ changed（方向变了）/ dropped（不做了）/ other（其他，默认）")
	verdict := fs.String("verdict", "", "evidence / review：一句结论（40 字以内）")
	details := fs.String("details", "", "evidence：实现细节（默认折叠）；review：怎么验的")
	blocksArg := fs.String("blocks", "", "evidence / review：表达块，JSON 数组或 JSON 文件路径")
	var todo, aside stringList
	fs.Var(&todo, "todo", "evidence：要你做的（机器验证不了的事），可重复")
	fs.Var(&aside, "aside", "evidence：顺带改动，可重复")
	var images, videos stringList
	fs.Var(&images, "image", "create / evidence / review：附图路径，可重复；evidence / review 还可以写成 JSON 对象 {\"path\": 路径, \"title\": 一句话标题, \"boxes\": [{\"x\", \"y\", \"w\", \"h\", \"label\", \"criterion\"}]}（框用 0～1 的比例坐标，criterion 可选：这个框证明第几条验收点）")
	shotsArg := fs.String("images", "", "evidence / review：带标题和改动框的附图，JSON 数组或 JSON 文件路径：[{\"path\", \"title\", \"boxes\"}]，按顺序显示，重点图放前面")
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
		// 用户附的文件下载到本机，每个附件带上 path（R-221）
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		c := client.New(2 * time.Minute)
		var out any
		if err := c.Do(ctx, "GET", "/agent/requirements/"+url.PathEscape(fs.Arg(0)), nil, &out); err != nil {
			return err
		}
		c.LocalizeFiles(ctx, out)
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return nil
	case "create":
		if *title == "" {
			return fmt.Errorf("用法：ks req create --title <标题> [--detail ...] [--acceptance ...]")
		}
		body := map[string]any{"title": *title, "detail": *detail, "acceptance": *acceptance, "status": *status, "priority": *priority, "from_decision": *fromDec, "session_key": key}
		if err := attachFiles(body, images); err != nil {
			return err
		}
		return agentCall("POST", "/agent/requirements", compact(body))
	case "edit":
		// 改需求内容（R-134）：只发给出的字段，没给的不改
		if fs.NArg() < 1 {
			return fmt.Errorf("用法：ks req edit <R-编号> [--title ...] [--detail ...] [--acceptance ...] [--priority P0-P3] [--type design|feature] [--reason 为什么改] [--approval D-x（进行中之后改验收标准必填）]")
		}
		body := editBody(fs, key, *reason, *approval)
		return agentCall("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/edit", body)
	case "status":
		if fs.NArg() < 2 {
			return fmt.Errorf("用法：ks req status <R-编号> <状态> [--reason 理由] [--reason-kind duplicate|changed|dropped|other]（PM / Adv 放行草稿：ks req status R-x ready --reason ...；搁置必须写 --reason）")
		}
		return agentCall("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/status", compact(map[string]any{"status": fs.Arg(1), "session_key": key, "reason": *reason, "reason_kind": *reasonKind}))
	case "request":
		// 申请改需求状态（R-188）：用户在审批抽屉里批准后由纸鸢改
		if fs.NArg() < 2 || strings.TrimSpace(*reason) == "" {
			return fmt.Errorf("用法：ks req request <R-编号> <ready|draft> --reason 一句理由（申请用户把草稿改为待开始，或重新打开已搁置的需求；用户批准后纸鸢直接改）")
		}
		return agentCall("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/status-request", map[string]any{"to": fs.Arg(1), "session_key": key, "reason": *reason})
	case "evidence":
		if fs.Arg(0) == "void" {
			// 作废交错的证据（R-189）：ks req evidence void <证据 id> --reason 理由
			if fs.NArg() < 2 || strings.TrimSpace(*reason) == "" {
				return fmt.Errorf("用法：ks req evidence void <证据 id> --reason 一句理由（证据 id 在 evidence 的返回或 ks req get 里；只有交它的会话或本项目的主调度 / 主助理能作废；交错的证据作废后重交，不要堆着）")
			}
			return agentCall("POST", "/agent/evidence/"+url.PathEscape(fs.Arg(1))+"/void", map[string]any{"session_key": key, "reason": *reason})
		}
		if fs.NArg() < 1 || fs.NArg() < 2 && *verdict == "" {
			return fmt.Errorf("用法：ks req evidence <R-编号> [证据正文] [--verdict 一句结论] [--blocks 块.json] [--todo ...] [--details ...] [--aside ...] [--kind test] [--link ...] [--image 截图.png 或 '{\"path\":…,\"title\":…,\"boxes\":[…]}'] [--images 附图.json] [--video 录屏.webm]\n%s\n表达块：%s", evidenceGuide, writing.BlocksGuide)
		}
		body := map[string]any{"kind": *kind, "summary": fs.Arg(1), "link": *link, "session_key": key, "verdict": *verdict, "details": *details}
		if len(todo) > 0 {
			body["todo"] = []string(todo)
		}
		if len(aside) > 0 {
			body["aside"] = []string(aside)
		}
		if err := attachFiles(body, videos); err != nil {
			return err
		}
		if err := attachShots(body, images, *shotsArg); err != nil {
			return err
		}
		raw, err := attachBlocks(body, *blocksArg)
		if err != nil {
			return err
		}
		out, err := agentCallOut("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/evidence", compact(body))
		if err != nil {
			return err
		}
		printBlockWarnings(out)
		printImageHints(out)
		if fs.Arg(1) != "" {
			printHint(writing.EvidenceHint(fs.Arg(1)))
		}
		printHint(writing.PayloadHint(*verdict, rawHasChecklist(raw)))
		return nil
	case "assign":
		// PM 派活（R-86）：ks req assign R-12 --to <Dev 会话键|auto> [--comment ...]
		if fs.NArg() < 1 || *to == "" {
			return fmt.Errorf("用法：ks req assign <R-编号> --to <Dev 会话键|auto> [--comment 交代] [--reason 放行理由（派草稿时必填）]")
		}
		return agentCall("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/assign", compact(map[string]any{"session_key": currentSession(*session), "to": *to, "comment": *comment, "reason": *reason}))
	case "review":
		// 审查员交审查意见 / 验证结果（R-88、R-232）：ks req review R-12 [意见] [--verdict 结论] [--blocks 清单.json] [--image 截图.png 或 JSON]；
		// 不改需求状态。review_submit 是旧版、没有 blocks / images 字段时（例如 Codex 上的旧 MCP）用它兜底
		if fs.NArg() < 1 || fs.NArg() < 2 && *verdict == "" {
			return fmt.Errorf("用法：ks req review <R-编号> [审查意见] [--verdict 一句结论] [--blocks 清单.json] [--details 怎么验的] [--image 截图.png 或 '{\"path\":…,\"title\":…,\"boxes\":[…]}'] [--images 附图.json]（只有 Rev 会话能交；验证结果用 checklist 逐条 pass / fail / ask）\n表达块：%s", writing.BlocksGuide)
		}
		body := map[string]any{"summary": fs.Arg(1), "session_key": currentSession(*session), "verdict": *verdict, "details": *details}
		if err := attachShots(body, images, *shotsArg); err != nil {
			return err
		}
		if _, err := attachBlocks(body, *blocksArg); err != nil {
			return err
		}
		out, err := agentCallOut("POST", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/reviews", compact(body))
		if err != nil {
			return err
		}
		printBlockWarnings(out)
		printImageHints(out)
		return nil
	case "link":
		if fs.NArg() < 1 || key == "" {
			return fmt.Errorf("用法：ks req link <R-编号> [--session tool:id]")
		}
		return agentCall("POST", "/agent/sessions/"+url.PathEscape(key)+"/link", map[string]any{"requirement": fs.Arg(0)})
	default:
		return fmt.Errorf("未知的 req 子命令：%s", sub)
	}
}

// runWithdraw：ks decision withdraw D-x [--reason 原因] [--replaced-by D-y]（R-136）。
func runWithdraw(args []string) error {
	fs := flag.NewFlagSet("decision withdraw", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	reason := fs.String("reason", "", "撤回原因")
	replaced := fs.String("replaced-by", "", "取代它的决策编号，如 D-142")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("用法：ks decision withdraw <D-编号> [--reason 原因] [--replaced-by D-y]（只能撤回自己提的、用户还没答复的决策）")
	}
	_, err := agentCallOut("POST", "/agent/decisions/"+url.PathEscape(fs.Arg(0))+"/withdraw", compact(map[string]any{"session_key": cliSession(*session), "reason": *reason, "replaced_by": *replaced}))
	return err
}

// runAnswer：ks decision answer D-x --answer 答复 --reason 理由 --risk low（R-163）：Adv 在用户的授权窗口内代答。
func runAnswer(args []string) error {
	fs := flag.NewFlagSet("decision answer", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	answer := fs.String("answer", "", "答复；有要拍板的点时是附言")
	reason := fs.String("reason", "", "为什么这样答、为什么不必等用户（必填）")
	risk := fs.String("risk", "", "你自己的风险判断：low / medium / high（必填），取它和提问方标的中更高的一档")
	var choices intList
	fs.Var(&choices, "choice", "有要拍板的点时逐点选第几个（从 1 开始），按点的顺序重复")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("用法：ks decision answer <D-编号> --answer 答复 --reason 理由 --risk low|medium|high [--choice 1 --choice 2]（只有被授权的 Adv 在用户的授权窗口内能用）")
	}
	body := map[string]any{"session_key": cliSession(*session), "answer": *answer, "reason": *reason, "risk": *risk}
	if len(choices) > 0 {
		cs := make([]int, len(choices))
		for i, c := range choices {
			cs[i] = c - 1
		}
		body["choices"] = cs
	}
	_, err := agentCallOut("POST", "/agent/decisions/"+url.PathEscape(fs.Arg(0))+"/answer", body)
	return err
}

func runDecision(args []string) error {
	if len(args) >= 1 && args[0] == "withdraw" {
		return runWithdraw(args[1:])
	}
	if len(args) >= 1 && args[0] == "answer" {
		return runAnswer(args[1:])
	}
	if len(args) < 1 || args[0] != "raise" {
		return fmt.Errorf("用法：ks decision withdraw <D-编号> [--reason 原因] [--replaced-by D-y]；ks decision answer <D-编号> --answer 答复 --reason 理由 --risk low（Adv 在授权窗口内代答）；ks decision raise --question <问题> [--context 说明] [--option A --option B] [--recommend ...] [--req R-1] [--risk low|medium|high] [--from D-x [--overturns]] [--blocks 块.json] [--points 要拍板的点.json] [--summary 方案要点]\n%s", decisionGuide)
	}
	fs := flag.NewFlagSet("decision raise", flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	question := fs.String("question", "", "问题：一句话，只问一件事（建议 60 字以内）")
	ctxText := fs.String("context", "", "说明：按“结论 / 依据 / 细节”写，别的待拍板的点另开决策（建议 400 字以内）")
	rec := fs.String("recommend", "", "推荐")
	req := fs.String("req", "", "关联需求")
	risk := fs.String("risk", "", "风险档 low / medium / high，不填按 high；产品方向、设计选型、语义 / 接口 / 权限变更、不可逆或对外操作一律 high")
	fromDec := fs.String("from", "", "来源决策（D-x）：接着哪条决策往下问，或要推翻哪条")
	overturns := fs.Bool("overturns", false, "这条要推翻 --from 那条决策的结论")
	var opts, optImgs stringList
	fs.Var(&opts, "option", "选项，可重复")
	fs.Var(&optImgs, "option-image", "选项的效果图路径，按 --option 的顺序一一对应，可重复")
	approval := fs.Bool("approval", false, "请用户批准（选项固定为批准 / 拒绝，两种都可以附言）")
	var imgs stringList
	fs.Var(&imgs, "image", "问题附带的图片路径（例如待批准的方案图），可重复")
	blocksArg := fs.String("blocks", "", "表达块，JSON 数组或 JSON 文件路径")
	pointsArg := fs.String("points", "", `要拍板的点，JSON 数组或 JSON 文件路径：[{"question":…,"options":[{"label":…,"desc":…,"recommended":true}]}]`)
	var summary stringList
	fs.Var(&summary, "summary", "方案要点，每条一行（以需求编号开头），可重复")
	if err := parseAnywhere(fs, args[1:]); err != nil {
		return err
	}
	if *question == "" {
		return fmt.Errorf("--question 不能为空")
	}
	body := map[string]any{"question": *question, "context": *ctxText, "recommendation": *rec, "requirement": *req, "risk": *risk, "from_decision": *fromDec, "session_key": cliSession(*session)}
	if *overturns {
		body["overturns"] = true
	}
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
	if _, err := attachBlocks(body, *blocksArg); err != nil {
		return err
	}
	if *pointsArg != "" {
		tmp := map[string]any{}
		if _, err := attachBlocks(tmp, *pointsArg); err != nil {
			return fmt.Errorf("--points：%w", err)
		}
		body["points"] = tmp["blocks"]
	}
	if len(summary) > 0 {
		body["summary"] = []string(summary)
	}
	out, err := agentCallOut("POST", "/agent/decisions", compact(body))
	if err != nil {
		return err
	}
	printBlockWarnings(out)
	printHint(writing.DecisionHint(*question, *ctxText))
	return nil
}

// 写作规范（R-110），与 MCP 工具说明一致。
const (
	evidenceGuide = "证据写法：第一句是结论（40 字以内，写用户能看到的变化）；逐条对照验收标准写“通过 / 未通过 / 需你确认”和一句依据，标上对应截图；“你需要做的”只写机器验证不了的事，没有就不写；风险可选；实现细节放最后；无关的顺带改动单列或另记需求。截图每张写一句话标题、用 boxes 框出改动的地方（框里写 criterion 标明证明第几条验收点，网页会裁成小图放在那条旁边；--image '{\"path\",\"title\",\"boxes\"}' 或 --images 附图.json），重点看的图放前面，补充、测试一类的放后面。交之前先自查截图里的文字有没有被截断、溢出或重叠。交错的证据用 ks req evidence void <证据 id> --reason 理由 作废后重交，不要堆着。" + writing.UIEvidenceGuide
	decisionGuide = "决策写法：问题一句话，每次只问一件事；一个方案里有几个点要分别拍板时用 --points 逐点给选项（标出推荐），不埋在说明里；说明按“结论 / 依据 / 细节”写，方案长时用 --summary 每条需求一行。" + writing.DecisionTableGuide
	stageGuide    = "小结只写跨需求的整体效果、遗留问题和你需要做的，不重复各条需求的证据。"
)

// printHint 输出字数提示（内容已保存，只提醒下次压缩）。
func printHint(h string) {
	if h != "" {
		fmt.Println(h)
	}
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
	on := fs.String("on", "", "还要被哪些项目事件唤醒（R-164）：decision（有人提决策）、review（交验收）、blocked（需求受阻）、stage（阶段交小结）、settings（用户在网页改了项目设置：放行开关、自动派发、参谋授权窗口）、status（用户在网页改了需求状态），逗号分隔；none 不订阅，default 按身份默认（Adv：decision,review,settings；PM：review,blocked,settings；其它不订阅）。不填沿用上次")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	q := url.Values{}
	if *on != "" {
		v, err := protocol.ParseWaitOn(*on)
		if err != nil {
			return err
		}
		q.Set("on", v)
	}
	if *timeout > 0 {
		q.Set("timeout", timeout.String())
	}
	q.Set("ppid", strconv.Itoa(os.Getppid()))
	// 先认出会话键并一直带着：daemon 重启（例如升级）后不再认得父进程，重连时要靠会话键
	if key := currentSession(*session); key != "" {
		q.Set("session_key", key)
	}
	// 先自查（R-117）：名下没有用户能在面板上推进的事时先提示，再照常等待；查不到时不提示
	var check struct {
		Hint    string `json:"hint"`
		Retired bool   `json:"retired"`
	}
	cctx, ccancel := context.WithTimeout(context.Background(), 8*time.Second)
	if err := client.New(8*time.Second).Do(cctx, "GET", "/wait/check?"+q.Encode(), nil, &check); err == nil && check.Hint != "" {
		fmt.Print(check.Hint)
	}
	ccancel()
	if check.Retired {
		return nil // 已交接卸任（R-224）：不再挂起等待
	}
	fmt.Fprintln(os.Stderr, "[Kitestring] 正在等用户在 Kitestring 上答复、验收或留言；命令运行期间请一直等着，不要中断。")
	if v := q.Get("on"); v != "" && v != protocol.OnNone && v != protocol.OnDefault {
		fmt.Fprintf(os.Stderr, "[Kitestring] 另外订阅了本项目的事件：%s；发生时也会唤醒你。\n", v)
	}
	// daemon 重启（例如部署升级）会断开连接：在截止时间内重连继续等，而不是报错把 Agent 白白唤醒（R-227）。
	ctx := signalContext()
	limit := daemon.MaxWait
	if *timeout > 0 {
		limit = min(*timeout, daemon.MaxWait)
	}
	res := waitLoop(ctx, time.Now().Add(limit), 3*time.Second, func(left time.Duration) (daemon.WaitResult, error) {
		if *timeout > 0 {
			q.Set("timeout", left.Round(time.Second).String())
		}
		var r daemon.WaitResult
		err := client.New(left+time.Minute).Do(ctx, "GET", "/wait?"+q.Encode(), nil, &r)
		return r, err
	})
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
		if res.Resident {
			fmt.Printf("[Kitestring] 等了 %s，用户没有新的答复。你需要常驻在线：重新运行 ks wait 继续待命%s。\n", waited, map[bool]string{true: "", false: "（在后台运行）"}[codex])
		} else if codex {
			fmt.Printf("[Kitestring] 等了 %s，用户没有新的答复。请结束本轮：用一两句话说明做到哪、在等用户什么；不要再运行 ks wait 续等。用户在 Kitestring 上答复后，会在下次对话时送达给你。\n", waited)
		} else {
			fmt.Printf("[Kitestring] 等了 %s，用户没有新的答复。可以结束本轮；用户回来后会在对话里继续。\n", waited)
		}
	case "stale":
		fmt.Println(res.Text)
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

// waitLoop 反复调用 call 挂起等待，直到拿到一个真正的结果或到截止时间（R-227）。
// 连接断了（daemon 重启、升级，call 返回错误），或 daemon 收尾时回了 canceled 而本进程并没有被取消，都当作断线：
// 隔 retry 再连，不把 Agent 唤醒。只有本进程被取消（Ctrl-C）才返回 canceled。
func waitLoop(ctx context.Context, deadline time.Time, retry time.Duration, call func(left time.Duration) (daemon.WaitResult, error)) daemon.WaitResult {
	for {
		left := time.Until(deadline)
		if left <= 0 {
			return daemon.WaitResult{Reason: "timeout"}
		}
		res, err := call(left)
		if ctx.Err() != nil {
			return daemon.WaitResult{Reason: "canceled"}
		}
		if err == nil && res.Reason != "canceled" {
			return res
		}
		select {
		case <-ctx.Done():
			return daemon.WaitResult{Reason: "canceled"}
		case <-time.After(min(retry, max(time.Until(deadline), 0))):
		}
	}
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
		return fmt.Errorf("用法：ks plan create --file plan.json | update <P-编号> --file plan.json | get <P-编号> | submit <P-编号> <阶段序号> <小结>")
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("plan "+sub, flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	file := fs.String("file", "", "create：计划结构 JSON 文件，- 表示从标准输入读")
	blocksArg := fs.String("blocks", "", "submit：表达块，JSON 数组或 JSON 文件路径")
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
	case "update":
		if fs.NArg() < 1 || *file == "" {
			return fmt.Errorf(`用法：ks plan update <P-编号> --file plan.json（用户退回计划后，按意见修改并重新提交；可只给要改的 title / goal / stages）`)
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
		return agentCall("PUT", "/agent/plans/"+url.PathEscape(fs.Arg(0)), body)
	case "get":
		if fs.NArg() < 1 {
			return fmt.Errorf("用法：ks plan get <P-编号>")
		}
		return agentCall("GET", "/agent/plans/"+url.PathEscape(fs.Arg(0)), nil)
	case "submit":
		if fs.NArg() < 3 {
			return fmt.Errorf("用法：ks plan submit <P-编号> <阶段序号> <阶段小结> [--blocks 块.json]\n%s", stageGuide)
		}
		seq, err := strconv.Atoi(fs.Arg(1))
		if err != nil {
			return fmt.Errorf("阶段序号应为数字：%s", fs.Arg(1))
		}
		body := map[string]any{"summary": fs.Arg(2), "session_key": key}
		if _, err := attachBlocks(body, *blocksArg); err != nil {
			return err
		}
		out, err := agentCallOut("POST", fmt.Sprintf("/agent/plans/%s/stages/%d/submit", url.PathEscape(fs.Arg(0)), seq), compact(body))
		if err != nil {
			return err
		}
		printBlockWarnings(out)
		return nil
	default:
		return fmt.Errorf("未知的 plan 子命令：%s", sub)
	}
}

// runCommit：需求关联的提交、回滚与 CI（R-156）。纸鸢只记录与展示，不改仓库、不触发 CI。
//
//	ks commit list <R-x>
//	ks commit link <R-x> <哈希> [--subject 标题]
//	ks commit ci <哈希> <pending|success|failure> [--url 链接]
func runCommit(args []string) error {
	const use = "用法：ks commit list <R-x> | link <R-x> <哈希> [--subject 标题] | ci <哈希> <pending|success|failure> [--url 链接]（标题末尾写了（R-x）的提交会自动关联，不用手动 link）"
	if len(args) < 1 {
		return fmt.Errorf(use)
	}
	sub, args := args[0], args[1:]
	fs := flag.NewFlagSet("commit "+sub, flag.ContinueOnError)
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	subject := fs.String("subject", "", "link：提交标题")
	link := fs.String("url", "", "ci：CI 结果链接")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	key := cliSession(*session)
	switch {
	case sub == "list" && fs.NArg() == 1:
		return agentCall("GET", "/agent/requirements/"+url.PathEscape(fs.Arg(0))+"/commits", nil)
	case sub == "link" && fs.NArg() == 2:
		return agentCall("POST", "/agent/commits", compact(map[string]any{"requirement": fs.Arg(0), "sha": fs.Arg(1), "subject": *subject, "session_key": key}))
	case sub == "ci" && fs.NArg() == 2:
		return agentCall("POST", "/agent/commits", compact(map[string]any{"sha": fs.Arg(0), "ci": fs.Arg(1), "ci_url": *link, "session_key": key}))
	}
	return fmt.Errorf(use)
}
