package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/store"
)

type assetReviewFlags struct {
	id, batch, category, status, kind, comment string
	approve, rework, dryRun, undo, json        bool
}

// assetReview：ks asset review（R-393 第 4 条）——用户授权的会话批量通过 / 返工本项目的素材，或撤销最近一次。
func assetReview(ctx context.Context, session string, refs []string, f assetReviewFlags) error {
	c := client.New(30 * time.Second)
	if f.undo {
		if f.approve || f.rework || len(refs) > 0 {
			return errors.New("--undo 单独用：ks asset review --undo")
		}
		var out struct {
			Restored []string          `json:"restored"`
			Kept     []store.AssetSkip `json:"kept"`
		}
		if err := c.Do(ctx, "POST", "/agent/asset-batches/undo", map[string]string{"session_key": session}, &out); err != nil {
			return err
		}
		if f.json {
			return printJSON(out)
		}
		fmt.Printf("已撤销最近一次批量审核：%d 件恢复为审核前的状态", len(out.Restored))
		if len(out.Restored) > 0 {
			fmt.Printf("（%s）", strings.Join(out.Restored, "、"))
		}
		fmt.Println("。")
		printSkips("之后又改过、没有还原", out.Kept)
		return nil
	}
	if f.approve == f.rework {
		return errors.New("要写 --approve（通过）或 --rework（返工）其中一个")
	}
	verdict := store.AssetApproved
	if f.rework {
		verdict = store.AssetRework
		if strings.TrimSpace(f.comment) == "" {
			return errors.New("返工要写 --comment 意见")
		}
	}
	body := map[string]any{"session_key": session, "verdict": verdict, "comment": f.comment, "dry_run": f.dryRun,
		"filter": map[string]string{"id": f.id, "batch": f.batch, "category": f.category, "status": f.status, "kind": f.kind}}
	if len(refs) > 0 {
		body["assets"] = refs
	} else if f.id == "" && f.batch == "" && f.category == "" && f.status == "" && f.kind == "" {
		return errors.New("要给素材（A-x 或素材 id），或筛选条件（--id 前缀* / --batch / --category / --status / --kind）\n" + assetUsage)
	}
	var out struct {
		Count    int               `json:"count"`
		Assets   []string          `json:"assets"`
		Skipped  []store.AssetSkip `json:"skipped"`
		Messages int               `json:"messages"`
	}
	if err := c.Do(ctx, "POST", "/agent/asset-batches", body, &out); err != nil {
		return err
	}
	if f.json {
		return printJSON(out)
	}
	label := assetStatusLabel[verdict]
	if f.dryRun {
		fmt.Printf("预检：会%s %d 件", label, out.Count)
	} else {
		fmt.Printf("已%s %d 件", label, out.Count)
	}
	if len(out.Assets) > 0 {
		fmt.Printf("（%s）", strings.Join(out.Assets, "、"))
	}
	if !f.dryRun && out.Count > 0 {
		fmt.Printf("，意见合并成 %d 条送给提交素材的会话；撤销用 ks asset review --undo", out.Messages)
	}
	fmt.Println("。")
	printSkips("跳过", out.Skipped)
	if f.dryRun {
		fmt.Println("（--dry-run：只预检，没有改）")
	}
	return nil
}

func printSkips(what string, xs []store.AssetSkip) {
	if len(xs) == 0 {
		return
	}
	fmt.Printf("%s %d 件：\n", what, len(xs))
	for _, x := range xs {
		fmt.Printf("  %s  %s  %s\n", x.Code, x.Name, x.Why)
	}
}
