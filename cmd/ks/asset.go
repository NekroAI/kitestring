package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"time"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

const assetUsage = `用法：
  ks asset push [manifest.json] --previews <目录> [--previews <目录>] --batch <批次> [--category 类别] [--status wip] [--req R-x] [--project 项目] [--branch 分支] [--commit 提交]
      把一批素材推进资源库：manifest 里的 GLB 是零件，预览目录里文件名以零件 id 开头的图挂到零件上，其余是独立的图（目录图、概念图……，文件名去扩展名当 id）。
      同一批次里同一 id 内容有变的重推记为新版本（旧版本保留），内容相同的不新增；Hub 已有的文件不重传；未连接 Hub 时排队，连上后自动补传。
  ks asset list [--batch 批次] [--status wip|pending|approved|rework] [--category 类别] [--project 项目 ID] [--json]
  ks asset get <A-x> [--json]   素材和它的全部版本
  ks asset queue                 本机排队等补传、或被 Hub 拒收的推送`

var assetStatusLabel = map[string]string{store.AssetWIP: "制作中", store.AssetPending: "待审", store.AssetApproved: "通过", store.AssetRework: "返工"}

func runAsset(args []string) error {
	if len(args) < 1 {
		return errors.New(assetUsage)
	}
	sub, args := args[0], args[1:]
	var previews stringList
	fs := flag.NewFlagSet("asset "+sub, flag.ContinueOnError)
	fs.Var(&previews, "previews", "push：预览图目录，可重复")
	batch := fs.String("batch", "", "push：批次（不写用 manifest 里的 batch）；list：只列这个批次")
	category := fs.String("category", "", "push：没写类别的素材用它；list：只列这个类别")
	status := fs.String("status", "", "push：推送后的状态 wip（制作中），默认 pending（待审）；list：只列这个状态")
	req := fs.String("req", "", "push：关联需求 R-x")
	project := fs.String("project", "", "push：记进哪个项目（项目名或 git remote），不写记进会话所在项目；list：项目 ID")
	branch := fs.String("branch", "", "push：分支（不写从 git 读）")
	commit := fs.String("commit", "", "push：提交（不写从 git 读）")
	asJSON := fs.Bool("json", false, "输出原始 JSON")
	session := fs.String("session", "", "会话键（tool:session_id），Claude 下可省略")
	if err := parseAnywhere(fs, args); err != nil {
		return err
	}
	allowed := map[string][]string{
		"push":  {"previews", "batch", "category", "status", "req", "project", "branch", "commit", "json"},
		"list":  {"batch", "category", "status", "project", "json"},
		"get":   {"json"},
		"queue": {"json"},
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
	case "push":
		if fs.NArg() > 1 {
			return errors.New(assetUsage)
		}
		p, notes, err := assetpush.Build(assetpush.Options{Manifest: fs.Arg(0), Previews: previews, Batch: *batch, Category: *category, Status: *status,
			Branch: *branch, Commit: *commit, Project: *project, Requirement: *req, SessionKey: cliSession(*session)})
		if err != nil {
			return err
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
	case "list":
		q := url.Values{}
		for k, v := range map[string]string{"batch": *batch, "category": *category, "status": *status, "project": *project, "session_key": cliSession(*session)} {
			if v != "" {
				q.Set(k, v)
			}
		}
		var out struct {
			Assets []store.Asset `json:"assets"`
		}
		if err := client.New(20*time.Second).Do(ctx, "GET", "/agent/assets?"+q.Encode(), nil, &out); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(out)
		}
		if len(out.Assets) == 0 {
			fmt.Println("资源库里没有符合条件的素材")
			return nil
		}
		for _, a := range out.Assets {
			fmt.Println(assetLine(a))
		}
		return nil
	case "get":
		if fs.NArg() != 1 {
			return errors.New(assetUsage)
		}
		var d store.AssetDetail
		if err := client.New(20*time.Second).Do(ctx, "GET", "/agent/assets/"+url.PathEscape(fs.Arg(0)), nil, &d); err != nil {
			return err
		}
		if *asJSON {
			return printJSON(d)
		}
		fmt.Println(assetLine(d.Asset))
		for _, v := range d.Versions {
			fmt.Printf("  第 %d 版 · %s · %s %s", v.Version, v.CreatedAt.Local().Format("01-02 15:04"), v.Branch, v.Commit)
			if v.File != nil {
				fmt.Printf(" · %s", v.File.Name)
			}
			if len(v.Previews) > 0 {
				fmt.Printf(" · 预览图 %d 张", len(v.Previews))
			}
			fmt.Println()
		}
		if len(d.Reviews) > 0 {
			fmt.Println("审核记录（新的在前）：")
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
			line := fmt.Sprintf("%s  %s · %d 条素材 · ", j.ID, j.Batch, j.Items)
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

func assetLine(a store.Asset) string {
	s := fmt.Sprintf("%s  %s/%s  第 %d 版 · %s", a.Code, a.Batch, a.Key, a.Version, assetStatusLabel[a.Status])
	if a.Category != "" {
		s += " · " + a.Category
	}
	if v := a.Latest; v != nil && v.Triangles > 0 {
		s += fmt.Sprintf(" · %d 面", v.Triangles)
	}
	return s
}
