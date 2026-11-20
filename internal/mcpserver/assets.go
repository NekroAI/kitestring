package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/NekroAI/kitestring/internal/assetpush"
	"github.com/NekroAI/kitestring/internal/client"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库（R-351）：asset_push 与 ks asset push 同一套整理逻辑（internal/assetpush），交给 daemon 推送或排队补传。
// R-376：manifest 是 asset.json 时按素材 id 推送，先本地逐件校验并报告，有错时整批不推（skip_invalid 只推合格的），dry_run 只校验。

type assetPushIn struct {
	Manifest    string   `json:"manifest,omitempty" jsonschema:"asset.json 或放着 asset.json 的目录（推荐，按素材 id 推送），或旧的 manifest.json 的本机路径；只推独立的图时可不填"`
	ID          string   `json:"id,omitempty" jsonschema:"asset.json：只推这个素材 id（末尾 * 按前缀，如 core:weapon.*）"`
	Previews    []string `json:"previews,omitempty" jsonschema:"预览图目录（本机路径），可多个"`
	Batch       string   `json:"batch,omitempty" jsonschema:"批次，如 G11e1。旧 manifest：不填用 manifest 里的 batch；asset.json：给每件素材都带上这个批次"`
	Category    string   `json:"category,omitempty" jsonschema:"旧 manifest：没写类别的素材用它；asset.json：只推这个类别"`
	Categories  string   `json:"categories,omitempty" jsonschema:"asset.json：类别允许表的路径；不填时用仓库里 .kitestring/assets.json 的 categories，没有就只校验非空和格式"`
	DryRun      bool     `json:"dry_run,omitempty" jsonschema:"只在本地校验、逐件报告，不推送"`
	SkipInvalid bool     `json:"skip_invalid,omitempty" jsonschema:"asset.json：有错的素材跳过，只推合格的（默认有错时整批不推）"`
	Status      string   `json:"status,omitempty" jsonschema:"推送后的状态：pending 待审（默认）/ wip 制作中"`
	Baseline    bool     `json:"baseline,omitempty" jsonschema:"申请标为已在用：游戏里已经在用的历史素材，推送后自动提一条批准类决策给用户，批准后整批记为通过（不进待审），拒绝则照常待审；只用于真正已在用的历史素材"`
	Requirement string   `json:"requirement,omitempty" jsonschema:"关联需求 R-x"`
	Project     string   `json:"project,omitempty" jsonschema:"记进哪个项目（项目名或 git remote）；不填记进会话所在项目"`
	Branch      string   `json:"branch,omitempty" jsonschema:"分支；不填从 git 读"`
	Commit      string   `json:"commit,omitempty" jsonschema:"提交；不填从 git 读"`
}

const assetPushDesc = `把素材推进纸鸢的资源库，用户在网页上集中浏览和审核（与 shell 命令 ks asset push 相同，字段细节见 ks help asset）。
推荐按 asset.json 推送：素材以带命名空间的稳定 id（如 core:weapon.carbine）为主键，批次是可多值的元数据。一件素材一个 asset.json（可传目录，逐个找 asset.json），或一个文件里 "assets": [...] 列多件。通用字段由纸鸢定义：id、category（允许的类别在项目的 .kitestring/assets.json 的 categories）、file（GLB、图片（含 SVG）、音效 OGG / WAV 或字体 TTF / OTF / WOFF2，不写时取 variants 里默认变体的文件）、previews（[{path, view}] 或路径）、batches、sources / source（来源）、license（许可）、variants（具名变体：默认变体就是素材本身，其余变体各是一条素材「id@变体名」，各有版本和审核，可写 file / files、previews）、display.name（可选的中文名，网页以它作主标题、id 作副标题并可按它搜；变体沿用素材的；本来有中文名才填，没有别为了填而编）；其它字段原样存进 meta，网页上展示。
音效的时长、声道、采样率、响度、峰值推送端从文件读（OGG 的响度要本机有 ffmpeg），读不出的写 audio：{"loudness_lufs": -18, "peak_dbfs": -1, "duration_s": 1.2}（文件读出的优先）；同一音效的多个版本写成 variants，网页上逐个试听。
字体（.ttf / .otf / .woff2）网页上显示字形预览；SVG 归独立的图，当图片显示（脚本不会执行）。两类都按内容校验，files 里有 font_regular 等多个字重时取 regular 当主文件。
预览图文件名写「<名字>__<机位>.png」，名字是 id 冒号后的部分或去掉类别前缀的部分（core:weapon.carbine → carbine），不写完整 id（冒号在 Windows 上不能用），如 carbine__front.png；项目配置的 previews_base（如 art/{category}/{name}）下 previews 目录里的图按所在目录归属；previews_base 空着时只按 previews 列表和 asset.json 所在目录找。不合命名、挂不上素材的图逐个列出，不推送。文件名含 Windows 不允许的字符（: * ? " < > | 等）或只差大小写的重名会提示。
历史素材（游戏里已经在用）可加 baseline 申请标为已在用：自动提一条批准类决策给用户，批准后整批记为通过，不用用户逐件点。
推送前先在本地逐件校验并报告；有错时整批不推（skip_invalid 只推合格的），dry_run 只校验。同一 id 内容有变的重推记为新版本，旧版本保留；内容相同的不新增版本，只并入新的批次。
旧的按批次推送仍可用：manifest.json 里的 GLB 是零件，预览目录里文件名以零件 id 开头的图挂到零件上，其余是独立的图。未连接 Hub 时排队，连上后自动补传。`

func (t *tools) registerAssets(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "asset_push", Description: assetPushDesc},
		func(ctx context.Context, req *mcp.CallToolRequest, in assetPushIn) (*mcp.CallToolResult, any, error) {
			if in.Manifest != "" && assetpush.IsAssetJSON(in.Manifest) {
				p, rep, err := assetpush.BuildByID(assetpush.IDOptions{Paths: []string{in.Manifest}, Previews: in.Previews, Batch: in.Batch, Categories: in.Categories, OnlyCat: in.Category, OnlyID: in.ID, SkipInvalid: in.SkipInvalid,
					Status: in.Status, Branch: in.Branch, Commit: in.Commit, Project: in.Project, Requirement: in.Requirement, SessionKey: t.sessionKey(ctx, req)})
				if err != nil {
					return nil, nil, err
				}
				report := assetpush.ReportText(rep)
				bad := rep.Bad()
				switch {
				case in.DryRun:
					return text(report + "（dry_run：只校验，没有推送）"), nil, nil
				case bad > 0 && !in.SkipInvalid:
					return text(report + fmt.Sprintf("有 %d 件素材有错，整批没有推送：改好后重推，或用 skip_invalid 只推合格的。", bad)), nil, nil
				case len(p.Items) == 0:
					return text(report + "没有合格的素材，没有推送。"), nil, nil
				}
				p.Baseline = in.Baseline
				var out protocol.AssetPushReply
				if err := client.New(15*time.Minute).Do(ctx, "POST", "/assets/push", p, &out); err != nil {
					return nil, nil, err
				}
				return text(report + assetpush.Text(p, out, nil)), nil, nil
			}
			p, notes, err := assetpush.Build(assetpush.Options{Manifest: in.Manifest, Previews: in.Previews, Batch: in.Batch, Category: in.Category, Status: in.Status,
				Branch: in.Branch, Commit: in.Commit, Project: in.Project, Requirement: in.Requirement, SessionKey: t.sessionKey(ctx, req)})
			if err != nil {
				return nil, nil, err
			}
			if in.DryRun {
				return text(assetpush.DryRunText(p, notes)), nil, nil
			}
			p.Baseline = in.Baseline
			var out protocol.AssetPushReply
			if err := client.New(15*time.Minute).Do(ctx, "POST", "/assets/push", p, &out); err != nil {
				return nil, nil, err
			}
			return text(assetpush.Text(p, out, notes)), nil, nil
		})
}
