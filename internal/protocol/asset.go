package protocol

import "encoding/json"

// 资源库（R-351）：Agent 用 ks asset push / MCP asset_push 把一批素材（manifest + 预览图 + GLB）推进资源库。
// 流向：CLI / MCP 读 manifest、配好预览图，把本机路径交给 daemon（POST /assets/push）；
// daemon 把文件按 sha256 复制进本机暂存区、记进队列，再查 Hub 已有哪些文件、只传缺的，最后提交这一批。
// Hub 连不上时这一批留在队列里，连上后自动补传；PushID 由 daemon 生成，补传重放时 Hub 按它去重。

// 素材条目的两种（AssetKind）。
const (
	AssetPart  = "part"  // GLB 零件：主文件是 GLB，挂它的预览图
	AssetImage = "image" // 独立的图：目录图、概念图、动作对比图，主文件就是图
)

// AssetFile 是素材里的一个文件。CLI → daemon 时写本机路径；daemon → Hub 时写 sha256 和 Hub 附件 ID。
type AssetFile struct {
	Path   string `json:"path,omitempty" doc:"本机路径（只在本机传给 daemon 时有）"`
	Name   string `json:"name" doc:"文件名"`
	SHA256 string `json:"sha256,omitempty" doc:"内容的 sha256（daemon 算）"`
	Size   int64  `json:"size,omitempty" doc:"字节数"`
	ID     string `json:"id,omitempty" doc:"Hub 附件 ID（先传到 /api/agent/assets/files，或已有同 sha256 的附件）"`
}

// AssetItem 是一条素材（manifest 里的一项，或预览目录里一张独立的图）。
type AssetItem struct {
	Key       string          `json:"key" doc:"素材 ID：manifest 的 id；独立的图用文件名（去扩展名）"`
	Kind      string          `json:"kind" enum:"part,image" doc:"part = GLB 零件，image = 独立的图"`
	Category  string          `json:"category,omitempty" doc:"类别（manifest 的 category，或目录图 / 概念图 / 对比图）"`
	Group     string          `json:"group,omitempty" doc:"同一用途的候选分组（概念图 A / B 版去掉末尾的 _A / _B）"`
	File      *AssetFile      `json:"file,omitempty" doc:"主文件：零件的 GLB，独立的图本身"`
	Previews  []AssetFile     `json:"previews,omitempty" doc:"零件的预览图"`
	Triangles int             `json:"triangles,omitempty" doc:"面数（三角形）"`
	Budget    int             `json:"budget,omitempty" doc:"面数预算上限"`
	Size      []float64       `json:"size,omitempty" doc:"尺寸（米，x / y / z）"`
	Pixels    []int           `json:"pixels,omitempty" doc:"图的像素尺寸（宽 / 高）"`
	Meta      json.RawMessage `json:"meta,omitempty" doc:"manifest 里这一项的原文"`
}

// AssetPush 是一次推送（一批素材）。
type AssetPush struct {
	PushID      string      `json:"push_id,omitempty" doc:"这次推送的 ID（daemon 生成），补传重放时按它去重"`
	SessionKey  string      `json:"session_key,omitempty" doc:"提交素材的会话"`
	Project     string      `json:"project,omitempty" doc:"记进哪个项目（项目名、git remote 或项目 ID）；不写记进会话所在项目"`
	Requirement string      `json:"requirement,omitempty" doc:"关联需求（R-x，可不填）"`
	Batch       string      `json:"batch" doc:"批次（如 G11e1）"`
	Branch      string      `json:"branch,omitempty" doc:"素材所在分支"`
	Commit      string      `json:"commit,omitempty" doc:"素材所在提交"`
	Status      string      `json:"status,omitempty" enum:"wip,pending," doc:"推送后的状态：pending 待审（默认）/ wip 制作中"`
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
	Items   []AssetPushItemResult `json:"items"`
	Reused  int                   `json:"reused" doc:"Hub 已有、没有重传的文件数（按 sha256 去重）"`
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
	Items     int    `json:"items"`
	State     string `json:"state" enum:"queued,failed" doc:"queued 等待补传 / failed 被 Hub 拒收（原因见 error）"`
	Error     string `json:"error,omitempty"`
	Tries     int    `json:"tries"`
	CreatedAt string `json:"created_at"`
}
