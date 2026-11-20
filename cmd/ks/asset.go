package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

const assetUsage = `用法：
  ks asset push <asset.json|目录>... [--category 类别] [--id 素材 id|前缀*] [--batch 批次] [--categories 类别表] [--previews <目录>] [--dry-run] [--skip-invalid] [--status wip] [--baseline] [--req R-x] [--project 项目]
      按 asset.json 推送（推荐）：素材以带命名空间的稳定 id（如 core:weapon.carbine）为主键，批次是可多值的元数据。
      给目录时逐个找下面的 asset.json；--category、--id 只推其中一部分；--batch 给每件素材再带上一个批次。
      推送前先在本地逐件校验并报告；有错时整批不推（--skip-invalid 只推合格的），--dry-run 只校验不推送。
      预览图文件名写「<名字>__<机位>.png」（名字不含冒号），挂不上的逐个列出。字段、项目配置和命名规则见 ks asset help。
      --baseline：申请标为已在用（游戏里已经在用的历史素材）。推送后自动提一条批准类决策给用户，批准后整批记为通过、不进待审，
      拒绝则照常待审；之后有新版本照常回到待审。只用于真正已在用的历史素材。
  ks asset push [manifest.json] --previews <目录> [--previews <目录>] --batch <批次> [--category 类别] [--dry-run] [--status wip] [--req R-x] [--project 项目] [--branch 分支] [--commit 提交]
      旧的按批次推送（仍可用）：manifest 里的 GLB 是零件，预览目录里文件名以零件 id 开头的图挂到零件上，其余是独立的图（目录图、概念图……，文件名去扩展名当 id）。
      同一批次里同一 id 内容有变的重推记为新版本（旧版本保留），内容相同的不新增；Hub 已有的文件不重传；未连接 Hub 时排队，连上后自动补传。
  ks asset summary [--project 项目] [--json]   只出统计：总数、各状态和各类别的数量、批次（几百字，先看它）
  ks asset list [--id 素材 id|前缀*] [--batch 批次] [--status wip|pending|approved|rework] [--category 类别] [--project 项目] [--limit N] [--offset N] [--all] [--json] [--full]
      默认查当前目录所在的项目（--project 填项目名、项目 ID 或 git remote 可改）；默认最多列 50 条，开头给总数和统计。
      筛选（--status / --category / --batch / --id 前缀*）比翻页更省；--offset 翻页，--all 取全部。
      --json 默认只给精简字段（id、类别、状态、版本、批次、预览图个数），--full 才给完整内容；不要整表拉 --json，先用 summary 和筛选。
  ks asset get <素材 id|A-x> [--history] [--json]   素材的最新版本和最近 3 条审核意见；--history 才给历史版本和全部审核记录
  ks asset queue                 本机排队等补传、或被 Hub 拒收的推送
  ks asset review <A-x|素材 id>... | [--id 前缀*] [--batch 批次] [--category 类别] [--status 状态] [--kind part|image|audio|font]  --approve | --rework --comment 意见  [--dry-run]
      批量通过 / 返工本项目的素材（只限用户在网页上授权了的会话，默认不开）：给素材或筛选条件，--dry-run 只预检件数。
      意见按收件会话合并成一条送达。ks asset review --undo 撤销本会话最近一次批量审核。
  ks asset supersede <旧条目> <新素材> | --map <对照表|-> | --clear <旧条目>... [--dry-run] [--project 项目] [--json]
      把旧条目标为「已被某件素材取代」（旧的按批次推送的条目迁到素材 id 后用）：旧条目保留、能看能审，网页和 list / get 显示取代关系；
      挂在批次 / 类别上的分享照常能看，并带上取代它的素材。有一条出错时一条都不改。对照表写法见 ks asset help。推错的条目只能在网页上删除。`

var assetStatusLabel = map[string]string{store.AssetWIP: "制作中", store.AssetPending: "待审", store.AssetApproved: "通过", store.AssetRework: "返工"}

func runAsset(args []string) error {
	if len(args) < 1 {
		return errors.New(assetUsage)
	}
	sub, args := args[0], args[1:]
	if sub == "help" || sub == "-h" || sub == "--help" {
		fmt.Print(assetUsage + "\n\n" + assetpush.FormatHelp + "\n" + assetpush.SupersedeMapHelp + "\n")
		return nil
	}
	var previews stringList
	fs := flag.NewFlagSet("asset "+sub, flag.ContinueOnError)
	fs.Var(&previews, "previews", "push：预览图目录，可重复")
	batch := fs.String("batch", "", "push：批次（不写用 manifest 里的 batch）；list：只列这个批次")
	category := fs.String("category", "", "push：没写类别的素材用它；list：只列这个类别")
	status := fs.String("status", "", "push：推送后的状态 wip（制作中），默认 pending（待审）；list：只列这个状态")
	req := fs.String("req", "", "push：关联需求 R-x")
	project := fs.String("project", "", "push：记进哪个项目（项目名或 git remote），不写记进会话所在项目；list / summary / get：查哪个项目（项目名、项目 ID 或 git remote），不写查当前目录所在的项目")
	limit := fs.Int("limit", assetListLimit, "list：最多列几条")
	offset := fs.Int("offset", 0, "list：跳过前几条（翻页）")
	all := fs.Bool("all", false, "list：不限条数，取全部")
	full := fs.Bool("full", false, "list --json：给完整内容（默认只给精简字段）")
	history := fs.Bool("history", false, "get：给历史版本和全部审核记录（默认只给最新版本和最近 3 条审核意见）")
	branch := fs.String("branch", "", "push：分支（不写从 git 读）")
	commit := fs.String("commit", "", "push：提交（不写从 git 读）")
	asJSON := fs.Bool("json", false, "输出原始 JSON")
	dryRun := fs.Bool("dry-run", false, "push：只在本地校验、报告，不推送；supersede：只校验，不改")
	skipInvalid := fs.Bool("skip-invalid", false, "push（asset.json）：有错的素材跳过，只推合格的")
	categories := fs.String("categories", "", "push（asset.json）：类别允许表（JSON 数组或 {\"categories\": [...]}）；不写时用仓库里 "+assetpush.ConfigFile+" 的 categories")
	id := fs.String("id", "", "list：素材 id（如 core:weapon.carbine）或编号 A-x，末尾带 * 按前缀查；push（asset.json）：只推这些")
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	baseline := fs.Bool("baseline", false, "push：申请标为已在用（历史素材），自动提决策给用户批准")
	approve := fs.Bool("approve", false, "review：批量通过")
	rework := fs.Bool("rework", false, "review：批量返工（要写 --comment）")
	comment := fs.String("comment", "", "review：共用的意见")
	kind := fs.String("kind", "", "review：只审这个类型（part / image / audio / font）")
	undo := fs.Bool("undo", false, "review：撤销本会话最近一次批量审核")
	mapFile := fs.String("map", "", "supersede：取代对照表（文件路径，- 读标准输入）")
	clear := fs.Bool("clear", false, "supersede：取消这些旧条目的取代标记")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	allowed := map[string][]string{
		"push":      {"previews", "batch", "category", "id", "status", "req", "project", "branch", "commit", "json", "dry-run", "skip-invalid", "categories", "baseline"},
		"review":    {"id", "batch", "category", "status", "kind", "approve", "rework", "comment", "dry-run", "undo", "json"},
		"list":      {"id", "batch", "category", "status", "project", "json", "limit", "offset", "all", "full"},
		"summary":   {"project", "json"},
		"get":       {"project", "json", "history"},
		"queue":     {"json"},
		"supersede": {"map", "clear", "project", "json", "dry-run"},
	}
	ok, known := allowed[sub]
	if !known {
		return errors.New(assetUsage)
	}
	if err := onlyFlags(fs, "ks asset "+sub, ok...); err != nil {
		return err
	}
	ctx := context.Background()
	switch sub {
	case "supersede":
		return runSupersede(ctx, fs.Args(), *mapFile, *clear, *dryRun, *asJSON, *project, *session)
	case "push":
		if *baseline && *status == store.AssetWIP {
			return errors.New("--baseline（申请标为已在用）不能和 --status wip 一起用")
		}
		if fs.NArg() >= 1 && assetpush.IsAssetJSON(fs.Arg(0)) {
			return pushByID(ctx, assetpush.IDOptions{Paths: fs.Args(), Previews: previews, Batch: *batch, Categories: *categories, OnlyCat: *category, OnlyID: *id, SkipInvalid: *skipInvalid,
				Status: *status, Branch: *branch, Commit: *commit, Project: *project, Requirement: *req, SessionKey: cliSession(*session)}, *baseline, *dryRun, *asJSON)
		}
		if fs.NArg() > 1 {
			return errors.New(assetUsage)
		}
		if *skipInvalid || *categories != "" || *id != "" {
			return errors.New("--skip-invalid、--categories、--id 只用于按 asset.json 推送")
		}
		p, notes, err := assetpush.Build(assetpush.Options{Manifest: fs.Arg(0), Previews: previews, Batch: *batch, Category: *category, Status: *status,
			Branch: *branch, Commit: *commit, Project: *project, Requirement: *req, SessionKey: cliSession(*session)})
		if err != nil {
			return err
		}
		p.Baseline = *baseline
		if *dryRun {
			if *asJSON {
				return printJSON(p)
			}
			fmt.Print(assetpush.DryRunText(p, notes))
			return nil
		}
		var out protocol.AssetPushReply
		if err := client.New(15*time.Minute).Do(ctx, "POST", "/assets/push", p, &out); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(out)
		}
		fmt.Print(assetpush.Text(p, out, notes))
		return nil
	case "review":
		return assetReview(ctx, cliSession(*session), fs.Args(), assetReviewFlags{id: *id, batch: *batch, category: *category, status: *status, kind: *kind,
			approve: *approve, rework: *rework, comment: *comment, dryRun: *dryRun, undo: *undo, json: *asJSON})
	case "summary":
		var out protocol.AssetSummary
		if err := client.New(20*time.Second).Do(ctx, "GET", "/agent/assets-summary?"+assetQuery(map[string]string{"project": *project}, *session).Encode(), nil, &out); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(out)
		}
		fmt.Print(summaryText(out))
		return nil
	case "list":
		lim := *limit
		if *all {
			lim = 0
		}
		if lim < 0 || *offset < 0 {
			return errors.New("--limit、--offset 不能是负数")
		}
		q := assetQuery(map[string]string{"id": *id, "batch": *batch, "category": *category, "status": *status, "project": *project}, *session)
		q.Set("limit", strconv.Itoa(lim))
		q.Set("offset", strconv.Itoa(*offset))
		var out struct {
			Assets []store.Asset     `json:"assets"`
			Total  int               `json:"total"`
			Facets store.AssetFacets `json:"facets"`
		}
		if err := client.New(60*time.Second).Do(ctx, "GET", "/agent/assets?"+q.Encode(), nil, &out); err != nil {
			return err
		}
		if *asJSON {
			if *full {
				return printJSON(map[string]any{"total": out.Total, "offset": *offset, "shown": len(out.Assets), "assets": out.Assets})
			}
			return printJSON(compactList(out.Total, *offset, out.Assets, out.Facets))
		}
		fmt.Print(listText(out.Total, *offset, out.Assets, out.Facets, lim > 0))
		return nil
	case "get":
		if fs.NArg() != 1 {
			return errors.New(assetUsage)
		}
		var d store.AssetDetail
		var q url.Values
		q = assetQuery(map[string]string{"project": *project}, *session)
		if err := client.New(20*time.Second).Do(ctx, "GET", "/agent/assets/"+url.PathEscape(fs.Arg(0))+"?"+q.Encode(), nil, &d); err != nil {
			return err
		}
		versions, reviews := len(d.Versions), len(d.Reviews)
		if !*history {
			d = trimDetail(d)
		}
		if *asJSON {
			return printJSON(d)
		}
		fmt.Println(assetLine(d.Asset))
		for _, s := range detailLinks(d) {
			fmt.Println("  " + s)
		}
		if v := d.Latest; v != nil {
			for _, s := range assetFacts(*v) {
				fmt.Println("  " + s)
			}
		}
		for _, v := range d.Versions {
			fmt.Printf("  第 %d 版 · %s · %s %s", v.Version, v.CreatedAt.Local().Format("01-02 15:04"), v.Branch, v.Commit)
			if v.File != nil {
				fmt.Printf(" · %s", v.File.Name)
			}
			if len(v.Previews) > 0 {
				fmt.Printf(" · 预览图 %d 张", len(v.Previews))
				if views := previewViews(v.Previews); len(views) > 0 {
					fmt.Printf("（%s）", strings.Join(views, "、"))
				}
			}
			fmt.Println()
		}
		if !*history && versions > len(d.Versions) {
			fmt.Printf("  （还有 %d 个更早的版本，--history 查看）\n", versions-len(d.Versions))
		}
		if len(d.Reviews) > 0 {
			if *history {
				fmt.Println("审核记录（新的在前）：")
			} else {
				fmt.Println("最近的审核意见（新的在前）：")
			}
			for _, r := range d.Reviews {
				fmt.Printf("  %s · 第 %d 版 · %s · %s", r.CreatedAt.Local().Format("01-02 15:04"), r.Version, assetStatusLabel[r.Verdict], reviewAuthor(r.Author))
				if r.Comment != "" {
					fmt.Printf("：%s", r.Comment)
				}
				if len(r.Files) > 0 {
					fmt.Printf("（附图 %d 张）", len(r.Files))
				}
				fmt.Println()
			}
			if !*history && reviews > len(d.Reviews) {
				fmt.Printf("  （还有 %d 条更早的审核记录，--history 查看）\n", reviews-len(d.Reviews))
			}
		}
		return nil
	default: // queue
		var out struct {
			Jobs []protocol.AssetJob `json:"jobs"`
		}
		if err := client.New(10*time.Second).Do(ctx, "GET", "/assets/queue", nil, &out); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(out)
		}
		if len(out.Jobs) == 0 {
			fmt.Println("没有排队或被拒收的推送")
			return nil
		}
		for _, j := range out.Jobs {
			what := j.Batch
			if j.ByID {
				what = "asset.json"
			}
			line := fmt.Sprintf("%s  %s · %d 条素材 · ", j.ID, what, j.Items)
			if j.State == "failed" {
				line += "被 Hub 拒收：" + j.Error
			} else {
				line += "等待补传"
				if j.Error != "" {
					line += fmt.Sprintf("（已试 %d 次：%s）", j.Tries, j.Error)
				}
			}
			fmt.Println(line)
		}
		return nil
	}
}

func printJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

// reviewAuthor：审核意见的作者（R-351）——用户 / 会话 / 访客（访客给 R-365 资源库分享预留）。
func reviewAuthor(x store.AssetAuthor) string {
	switch x.Kind {
	case store.SubmitterUser:
		return "用户"
	case store.SubmitterGuest:
		return "访客「" + x.Name + "」"
	case store.SubmitterSession:
		if x.Name != "" {
			return "会话「" + x.Name + "」"
		}
		return "会话"
	}
	return "未知"
}

// pushByID 按 asset.json 推送（R-376）：先本地逐件校验并报告；有错时不推（--skip-invalid 只推合格的），--dry-run 只校验。
func pushByID(ctx context.Context, o assetpush.IDOptions, baseline, dryRun, asJSON bool) error {
	p, rep, err := assetpush.BuildByID(o)
	if err != nil {
		return err
	}
	p.Baseline = baseline
	bad := rep.Bad()
	var stop error
	switch {
	case bad > 0 && !o.SkipInvalid:
		stop = fmt.Errorf("有 %d 件素材有错，整批没有推送：改好后重推，或加 --skip-invalid 只推合格的", bad)
	case len(p.Items) == 0:
		stop = errors.New("没有合格的素材，没有推送")
	}
	if dryRun || stop != nil {
		if asJSON {
			if err := printJSON(map[string]any{"report": rep, "pushed": false}); err != nil {
				return err
			}
		} else {
			fmt.Print(assetpush.ReportText(rep))
		}
		if dryRun {
			if !asJSON {
				fmt.Println("（--dry-run：只校验，没有推送）")
			}
			if bad > 0 {
				return fmt.Errorf("有 %d 件素材有错", bad)
			}
			return nil
		}
		return stop
	}
	if !asJSON {
		fmt.Print(assetpush.ReportText(rep))
	}
	var out protocol.AssetPushReply
	if err := client.New(15*time.Minute).Do(ctx, "POST", "/assets/push", p, &out); err != nil {
		return err
	}
	if asJSON {
		return printJSON(map[string]any{"report": rep, "pushed": true, "reply": out})
	}
	fmt.Print(assetpush.Text(p, out, nil))
	if bad > 0 {
		fmt.Printf("有错的 %d 件已跳过，没有推送（见上面的校验报告）。\n", bad)
	}
	return nil
}

func previewViews(ps []store.AssetPreview) []string {
	var out []string
	for _, p := range ps {
		if p.View != "" && !slices.Contains(out, p.View) {
			out = append(out, p.View)
		}
	}
	return out
}

// assetFacts：素材 id 推送的通用字段（批次、来源、许可、变体、项目字段）。
func assetFacts(v store.AssetVersion) []string {
	var out []string
	if v.Audio != nil {
		out = append(out, "音效："+assetpush.AudioText(v.Audio))
	}
	if v.Source != "" || v.License != "" {
		out = append(out, "来源 "+orDash(v.Source)+" · 许可 "+orDash(v.License))
	}
	if len(v.Variants) > 0 {
		out = append(out, "变体（原文）："+string(v.Variants))
	}
	if len(v.Meta) > 0 {
		out = append(out, "项目字段："+string(v.Meta))
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func assetLine(a store.Asset) string {
	name := a.Batch + "/" + a.Key
	if a.UID != "" {
		name = a.UID
	}
	s := fmt.Sprintf("%s  %s  第 %d 版 · %s", a.Code, name, a.Version, assetStatusLabel[a.Status])
	if a.Category != "" {
		s += " · " + a.Category
	}
	if a.UID != "" && len(a.Batches) > 0 {
		s += " · 批次 " + strings.Join(a.Batches, "、")
	}
	if v := a.Latest; v != nil && v.Triangles > 0 {
		s += fmt.Sprintf(" · %d 面", v.Triangles)
	}
	if v := a.Latest; v != nil && v.Audio != nil {
		s += " · 音效 " + assetpush.AudioText(v.Audio)
	}
	return s + linkNote(a)
}
