package protocol

import (
	"encoding/json"
	"regexp"
	"strings"
)

// 资源库（R-351）：Agent 用 ks asset push / MCP asset_push 把一批素材（manifest + 预览图 + GLB）推进资源库。
// 流向：CLI / MCP 读 manifest、配好预览图，把本机路径交给 daemon（POST /assets/push）；
// daemon 把文件按 sha256 复制进本机暂存区、记进队列，再查 Hub 已有哪些文件、只传缺的，最后提交这一批。
// Hub 连不上时这一批留在队列里，连上后自动补传；PushID 由 daemon 生成，补传重放时 Hub 按它去重。

// 素材条目的两种（AssetKind）。
const (
	AssetPart  = "part"  // GLB 零件：主文件是 GLB，挂它的预览图
	AssetImage = "image" // 独立的图：目录图、概念图、动作对比图，主文件就是图
	AssetAudio = "audio" // 音效（R-381）：主文件是 OGG（Vorbis / Opus）或 WAV，可挂预览图当封面
	AssetFont  = "font"  // 字体（R-387）：主文件是 TTF / OTF / WOFF2，网页上显示字形预览；SVG 归独立的图
)

// AssetAudioInfo 是音效素材的音频信息（R-381）。能从文件读的由推送端读（WAV 全部；OGG 读文件头，响度、峰值、波形要本机有 ffmpeg 解码），
// 读不出的可在 asset.json 的 audio 里声明；读出的优先，声明只补读不出的，Declared 记下哪些取自声明。
type AssetAudioInfo struct {
	Codec      string   `json:"codec,omitempty" doc:"编码：pcm（WAV）/ vorbis / opus"`
	Duration   float64  `json:"duration_s,omitempty" doc:"时长（秒）"`
	Channels   int      `json:"channels,omitempty" doc:"声道数"`
	SampleRate int      `json:"sample_rate,omitempty" doc:"采样率（Hz）"`
	Loudness   *float64 `json:"loudness_lufs,omitempty" doc:"综合响度（LUFS，ITU-R BS.1770）"`
	Peak       *float64 `json:"peak_dbfs,omitempty" doc:"采样峰值（dBFS）"`
	Wave       []int    `json:"wave,omitempty" doc:"波形：均分成若干段，每段的峰值（0–100，满幅 100），没有预览图时画成封面"`
	Declared   []string `json:"declared,omitempty" doc:"取自 asset.json 声明（文件里读不出）的字段名"`
}

// AssetFile 是素材里的一个文件。CLI → daemon 时写本机路径；daemon → Hub 时写 sha256 和 Hub 附件 ID。
type AssetFile struct {
	Path   string `json:"path,omitempty" doc:"本机路径（只在本机传给 daemon 时有）"`
	Name   string `json:"name" doc:"文件名"`
	SHA256 string `json:"sha256,omitempty" doc:"内容的 sha256（daemon 算）"`
	Size   int64  `json:"size,omitempty" doc:"字节数"`
	ID     string `json:"id,omitempty" doc:"Hub 附件 ID（先传到 /api/agent/assets/files，或已有同 sha256 的附件）"`
	View   string `json:"view,omitempty" doc:"预览图的机位（R-376：文件名「<名字>__<机位>.png」里的机位），网页按它分组"`
}

// AssetItem 是一条素材（manifest 里的一项，或预览目录里一张独立的图）。
type AssetItem struct {
	Key       string          `json:"key" doc:"素材 ID：manifest 的 id；独立的图用文件名（去扩展名）"`
	Kind      string          `json:"kind" enum:"part,image,audio,font" doc:"part = GLB 零件，image = 独立的图（含 SVG，R-387），audio = 音效（R-381），font = 字体（TTF / OTF / WOFF2，R-387）"`
	Category  string          `json:"category,omitempty" doc:"类别（manifest 的 category，或目录图 / 概念图 / 对比图）"`
	Group     string          `json:"group,omitempty" doc:"同一用途的候选分组（概念图 A / B 版去掉末尾的 _A / _B）"`
	Name      string          `json:"name,omitempty" doc:"R-400：素材的中文名（asset.json 的 display.name，可选，推送方按需传）；有时网页以它作主标题、素材 id 作副标题，可按它搜索"`
	File      *AssetFile      `json:"file,omitempty" doc:"主文件：零件的 GLB，独立的图本身"`
	Previews  []AssetFile     `json:"previews,omitempty" doc:"零件的预览图"`
	Triangles int             `json:"triangles,omitempty" doc:"面数（三角形）"`
	Budget    int             `json:"budget,omitempty" doc:"面数预算上限"`
	Size      []float64       `json:"size,omitempty" doc:"尺寸（米，x / y / z）"`
	Pixels    []int           `json:"pixels,omitempty" doc:"图的像素尺寸（宽 / 高）"`
	Audio     *AssetAudioInfo `json:"audio,omitempty" doc:"音效的时长、声道、响度等（R-381）"`
	Meta      json.RawMessage `json:"meta,omitempty" doc:"旧 manifest：这一项的原文；asset.json：纸鸢不认识的项目特有字段（原样）"`
	// 以下是 asset.json 推送（R-376）的通用字段；旧的按批次推送不写
	Batches  []string        `json:"batches,omitempty" doc:"asset.json：这次带上的批次（可多个，并入素材已有的批次，不删旧的）"`
	Source   string          `json:"source,omitempty" doc:"asset.json：来源（自制、外包、网址……）"`
	License  string          `json:"license,omitempty" doc:"asset.json：许可（CC0、自有……）"`
	Variants json.RawMessage `json:"variants,omitempty" doc:"asset.json：具名变体的原文（各变体另作一条推送，见 Parent）"`
	// R-380：具名变体各自有版本和审核。变体是单独的一条：Key = 「素材 id@变体名」，Parent 是素材 id，Variant 是变体名；
	// 素材本身（默认变体）Parent 为空、Variant 是默认变体名
	Variant string `json:"variant,omitempty" doc:"asset.json：变体名（素材本身写默认变体名）"`
	Parent  string `json:"parent,omitempty" doc:"asset.json：这条是哪个素材 id 的变体；素材本身不写"`
}

// AssetPush 是一次推送（一批素材）。
type AssetPush struct {
	PushID      string      `json:"push_id,omitempty" doc:"这次推送的 ID（daemon 生成），补传重放时按它去重"`
	SessionKey  string      `json:"session_key,omitempty" doc:"提交素材的会话"`
	Project     string      `json:"project,omitempty" doc:"记进哪个项目（项目名、git remote 或项目 ID）；不写记进会话所在项目"`
	Requirement string      `json:"requirement,omitempty" doc:"关联需求（R-x，可不填）"`
	ByID        bool        `json:"by_id,omitempty" doc:"R-376：按 asset.json 推送，素材以带命名空间的稳定 id（如 core:weapon.carbine）为主键，key 就是 id，批次只是元数据；不写是旧的按批次推送"`
	Batch       string      `json:"batch" doc:"批次（如 G11e1）；按 id 推送时可空（批次写在每条素材的 batches 里）"`
	Branch      string      `json:"branch,omitempty" doc:"素材所在分支"`
	Commit      string      `json:"commit,omitempty" doc:"素材所在提交"`
	Status      string      `json:"status,omitempty" enum:"wip,pending," doc:"推送后的状态：pending 待审（默认）/ wip 制作中"`
	Baseline    bool        `json:"baseline,omitempty" doc:"R-393：申请标为已在用（游戏里已经在用的历史素材）：推送后先挂在一条申请上，同时提一条批准类决策给用户，批准后整批记为通过，拒绝则是普通待审"`
	Items       []AssetItem `json:"items" doc:"素材"`
}

// AssetPushItemResult 是一条素材的推送结果。
type AssetPushItemResult struct {
	Key     string `json:"key"`
	Code    string `json:"code" doc:"素材编号 A-x"`
	Version int    `json:"version" doc:"现在的最新版本号"`
	Result  string `json:"result" enum:"created,updated,unchanged" doc:"created 新素材 / updated 记为新版本 / unchanged 内容与最新版相同，未新增版本"`
}

// AssetPushResult 是 Hub 对一次推送的答复。
type AssetPushResult struct {
	PushID  string                `json:"push_id"`
	Project string                `json:"project" doc:"项目名"`
	Batch   string                `json:"batch"`
	ByID    bool                  `json:"by_id,omitempty" doc:"按 asset.json（素材 id）推送"`
	Items   []AssetPushItemResult `json:"items"`
	Reused  int                   `json:"reused" doc:"Hub 已有、没有重传的文件数（按 sha256 去重）"`
	// R-393：--baseline 推送时的申请
	Baseline *AssetBaselineInfo `json:"baseline,omitempty" doc:"申请标为已在用（--baseline）的结果"`
}

// AssetBaselineInfo 是一次「申请标为已在用」：哪条决策在等用户批准、申请了几件。
type AssetBaselineInfo struct {
	Decision string `json:"decision,omitempty" doc:"等用户批准的决策 D-x；没有可申请的素材时为空"`
	Count    int    `json:"count" doc:"申请标为已在用的件数（仍在待审 / 制作中的；已通过、返工中的不申请）"`
	Note     string `json:"note,omitempty" doc:"没有提申请的原因"`
}

// AssetPushReply 是 daemon 回给 CLI / MCP 的结果：要么已入库（Result），要么排队等补传（Queued）。
type AssetPushReply struct {
	Queued bool             `json:"queued"`
	JobID  string           `json:"job_id"`
	Reason string           `json:"reason,omitempty" doc:"排队的原因"`
	Result *AssetPushResult `json:"result,omitempty"`
}

// AssetJob 是 daemon 队列里一批待补传的素材（ks asset queue 列出）。
type AssetJob struct {
	ID        string `json:"id"`
	Batch     string `json:"batch"`
	ByID      bool   `json:"by_id,omitempty" doc:"按 asset.json（素材 id）推送"`
	Items     int    `json:"items"`
	State     string `json:"state" enum:"queued,failed" doc:"queued 等待补传 / failed 被 Hub 拒收（原因见 error）"`
	Error     string `json:"error,omitempty"`
	Tries     int    `json:"tries"`
	CreatedAt string `json:"created_at"`
}

// R-376：素材 id 的格式是「命名空间:路径」，如 core:weapon.carbine。
// 命名空间用小写字母、数字、_ . -；路径用字母、数字、_ . - /（/ 分层）。id 带冒号，所以预览图文件名不写完整 id，
// 只写冒号后的名字（「<名字>__<机位>.png」），Windows 上也能用。
var (
	assetNSRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]*$`)
	assetPathRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
)

// CheckAssetID 校验素材 id，合格返回空，不合格返回一句原因（接在 id 后面）。
func CheckAssetID(id string) string {
	ns, path, ok := strings.Cut(id, ":")
	switch {
	case !ok:
		return "要带命名空间，写成「命名空间:名字」，如 core:weapon.carbine"
	case len(id) > 128:
		return "太长（最多 128 个字符）"
	case !assetNSRe.MatchString(ns):
		return "的命名空间只能用小写字母、数字和 _ . -，且以字母或数字开头"
	case !assetPathRe.MatchString(path) || strings.Contains(path, "//") || strings.HasSuffix(path, "/") || strings.HasSuffix(path, "."):
		return "冒号后只能用字母、数字和 _ . - /，不能以 / 或 . 结尾"
	}
	return ""
}

// VariantSep 连接素材 id 和变体名：core:weapon.carbine@desert（R-380）。素材 id 里不会有 @。
const VariantSep = "@"

var assetVariantRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// VariantKey 是变体的 key（也是它的素材 id）。
func VariantKey(id, name string) string { return id + VariantSep + name }

// CheckVariantName 校验变体名，合格返回空，不合格返回一句原因（接在变体名后面）。
func CheckVariantName(name string) string {
	switch {
	case name == "":
		return "不能为空"
	case len(name) > 64:
		return "太长（最多 64 个字符）"
	case !assetVariantRe.MatchString(name) || strings.HasSuffix(name, "."):
		return "只能用字母、数字和 _ . -，不能以 . 结尾"
	}
	return ""
}

// AssetName 是素材 id 冒号后的名字（预览图文件名「<名字>__<机位>.png」里的名字）；路径带 / 时取最后一段。
func AssetName(id string) string {
	_, path, ok := strings.Cut(id, ":")
	if !ok {
		path = id
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		path = path[i+1:]
	}
	return path
}

// AssetSummary 是一个项目资源库的统计（R-391）：Agent 查询先看它，再决定要不要筛选列表。
type AssetSummary struct {
	Project    string         `json:"project" doc:"项目名；没确定项目时为空（统计全部项目）"`
	Total      int            `json:"total"`
	Statuses   map[string]int `json:"statuses" doc:"各状态的素材数"`
	Categories map[string]int `json:"categories" doc:"各类别的素材数"`
	Batches    []string       `json:"batches" doc:"全部批次"`
}
