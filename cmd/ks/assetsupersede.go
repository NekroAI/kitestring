package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

// ks asset supersede（R-380）：把旧条目标为被某件素材取代。旧条目照常保留；网页、list / get 显示取代关系。

type supersedeReply struct {
	Project string                  `json:"project"`
	Applied bool                    `json:"applied"`
	Results []store.SupersedeResult `json:"results"`
}

func runSupersede(ctx context.Context, args []string, mapFile string, clear, dryRun, asJSON bool, project, session string) error {
	var pairs []assetpush.SupersedePair
	switch {
	case mapFile != "":
		if len(args) > 0 || clear {
			return errors.New("--map 和直接写「旧 新」、--clear 不能一起用")
		}
		var raw []byte
		var err error
		if mapFile == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(mapFile)
		}
		if err != nil {
			return fmt.Errorf("读不到对照表：%w", err)
		}
		if pairs, err = assetpush.ParseSupersedeMap(string(raw)); err != nil {
			return err
		}
	case clear:
		if len(args) == 0 {
			return errors.New("用法：ks asset supersede --clear <旧条目>...")
		}
		for _, a := range args {
			pairs = append(pairs, assetpush.SupersedePair{Old: a})
		}
	default:
		if len(args) != 2 {
			return errors.New("用法：ks asset supersede <旧条目> <新素材>，或 --map <对照表>（写法见 ks asset help）")
		}
		pairs = []assetpush.SupersedePair{{Old: args[0], New: args[1]}}
	}
	body := map[string]any{"pairs": pairs, "dry_run": dryRun}
	q := assetQuery(map[string]string{"project": project}, session)
	for _, k := range []string{"project", "session_key", "cwd_remote"} {
		if v := q.Get(k); v != "" {
			body[k] = v
		}
	}
	var out supersedeReply
	if err := client.New(60*time.Second).Do(ctx, "POST", "/agent/assets/supersede", body, &out); err != nil {
		return err
	}
	if asJSON {
		if err := printJSON(out); err != nil {
			return err
		}
	} else {
		fmt.Print(supersedeText(out, dryRun))
	}
	bad := 0
	for _, r := range out.Results {
		if r.Result == "error" {
			bad++
		}
	}
	if bad > 0 {
		return fmt.Errorf("有 %d 条出错，一条都没有改", bad)
	}
	return nil
}

func refName(r *store.AssetRef, raw string) string {
	if r == nil {
		return raw
	}
	name := r.UID
	if name == "" {
		name = r.Batch + "/" + r.Key
	}
	return r.Code + " " + name
}

func supersedeText(out supersedeReply, dryRun bool) string {
	var b strings.Builder
	label := map[string]string{"set": "标记取代", "unchanged": "没有变化", "cleared": "取消取代", "error": "有错"}
	n := map[string]int{}
	for _, r := range out.Results {
		n[r.Result]++
		line := "  " + label[r.Result] + "  " + refName(r.OldRef, r.Old)
		if r.New != "" {
			line += " → " + refName(r.NewRef, r.New)
		}
		if r.Replace != nil {
			line += "（原来是 " + refName(r.Replace, "") + "）"
		}
		if r.Error != "" {
			line += "：" + r.Error
		}
		b.WriteString(line + "\n")
	}
	where := ""
	if out.Project != "" {
		where = "项目「" + out.Project + "」："
	}
	switch {
	case n["error"] > 0:
		fmt.Fprintf(&b, "%s%d 条里有 %d 条出错，一条都没有改。\n", where, len(out.Results), n["error"])
	case dryRun:
		fmt.Fprintf(&b, "%s校验通过，%d 条（--dry-run：没有改）。\n", where, len(out.Results))
	default:
		fmt.Fprintf(&b, "%s标记取代 %d 条，取消取代 %d 条，没有变化 %d 条。\n", where, n["set"], n["cleared"], n["unchanged"])
	}
	return b.String()
}

// detailLinks：ks asset get 里的变体和取代关系（R-380）。
func detailLinks(d store.AssetDetail) []string {
	var out []string
	if len(d.Variants) > 0 {
		var xs []string
		for _, v := range d.Variants {
			xs = append(xs, v.Variant+"（"+v.Code+" "+assetStatusLabel[v.Status]+"）")
		}
		def := ""
		if d.Variant != "" {
			def = "默认变体 " + d.Variant + "；"
		}
		out = append(out, fmt.Sprintf("变体 %d 个：%s其余 %s", len(d.Variants)+1, def, strings.Join(xs, "、")))
	}
	if len(d.Supersedes) > 0 {
		var xs []string
		for i := range d.Supersedes {
			xs = append(xs, refName(&d.Supersedes[i], ""))
		}
		out = append(out, "取代了："+strings.Join(xs, "、"))
	}
	return out
}
