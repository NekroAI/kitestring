package hub

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/NekroAI/kitestring/internal/blocks"
	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
	"github.com/NekroAI/kitestring/internal/writing"
)

// 需求留言（R-249）：会话把细节写在需求下（ks req comment / req_comment），消息只做一句话通知、请对方来看。
// 留言和用户留言进同一条时间线，署名是留言的会话（身份 + 会话名）；留言记录本身不送达，订阅了这条需求的会话收到一句只告知的提醒（R-249 P4）。
// 署名和团队消息一样由当前会话决定（R-248）：ks 命令自动识别，识别不到时才接受 --session，并由这里校验它是没结束的会话。

type commentOut struct {
	Body struct {
		Code          string   `json:"code" doc:"这条留言的编号（D-x）"`
		Requirement   string   `json:"requirement" doc:"留言所在的需求编号"`
		Message       string   `json:"message"`
		Hint          string   `json:"hint,omitempty" doc:"字数提示：超过 600 字时照常保存，提示压缩"`
		BlockWarnings []string `json:"block_warnings,omitempty" doc:"没有保存的块及原因（其余内容已保存）"`
	}
}

func (h *Hub) registerAgentComment(api huma.API) {
	huma.Register(api, huma.Operation{OperationID: "agent-comment", Method: http.MethodPost, Path: "/api/agent/requirements/{ref}/comments", Tags: []string{"agent"},
		Summary:     "在需求下留言（R-249）：会话把细节写在这里，消息只放编号加一句话",
		Description: "留言进需求时间线，和用户留言放在一起、署名区分（身份 + 会话名）；req_get 读得到最新几条，用户在网页上看得到。留言记录本身不送达；订阅了这条需求的会话（含关联它的）收到一句只告知的提醒，要别人立刻来看另用 ks team tell 发一句话。署名永远是当前会话（session_key），不能指定别的会话。超过 600 字照常保存，返回里提示压缩。"},
		h.postComment)
}

type commentIn struct {
	Ref  string `path:"ref"`
	Body struct {
		SessionKey     string   `json:"session_key" minLength:"1" doc:"留言的会话（调用者）"`
		Text           string   `json:"text,omitempty" doc:"留言正文：先结论，再分条写细节"`
		Files          []string `json:"files,omitempty" doc:"附图 ID（先上传到 /api/agent/files）"`
		Blocks         []any    `json:"blocks,omitempty" doc:"表达块（R-97），同证据：checklist / compare / figure / flow / metrics / scope / options / table；不合法的块丢掉并在 block_warnings 里说明，留言照常保存"`
		SessionClaimed bool     `json:"session_claimed,omitempty" doc:"session_key 是调用方用 --session 自报的（自动识别不到当前会话时，R-248）：hub 校验它是没结束、属于某个项目的会话"`
		AgentID        string   `json:"agent_id,omitempty" doc:"留言的子任务（一次性子代理）的 agent_id；会话主线程为空"`
	}
}

// postComment 记一条会话留言。
func (h *Hub) postComment(ctx context.Context, in *commentIn) (*commentOut, error) {
	b := in.Body
	me, err := h.store.GetSession(ctx, b.SessionKey)
	if err != nil {
		return nil, apiErr(err)
	}
	if b.SessionClaimed && (me.State == store.StateEnded || me.State == store.StateLost || me.DoneAt != nil || me.ProjectID == "") {
		return nil, huma.Error409Conflict("--session " + me.Key + " 不是一个没结束、属于某个项目的会话，不能据此署名，留言没有保存")
	}
	r, err := h.store.GetRequirement(ctx, in.Ref)
	if err != nil {
		return nil, apiErr(err)
	}
	files, err := h.store.ResolveFiles(ctx, b.Files)
	if err != nil {
		return nil, err
	}
	bs, warns := blocks.Parse(b.Blocks, h.blockFile(ctx))
	text := strings.TrimSpace(b.Text)
	if text == "" && len(files) == 0 && len(bs) == 0 {
		return nil, huma.Error400BadRequest("请写下留言（text），或附上图片 / 表达块")
	}
	d, err := h.store.CreateComment(ctx, r, store.Comment{By: me.Key, Role: me.Role, Agent: strings.TrimSpace(b.AgentID), Text: text, Files: files, Blocks: bs})
	if err != nil {
		return nil, apiErr(err)
	}
	h.broker.publish("requirements")
	h.reqEvent(ctx, r, evComment, me.Key, "有新留言 "+d.Code) // 订阅了这条需求的会话收到一句话提醒（R-249 P4）
	out := &commentOut{}
	out.Body.Code, out.Body.Requirement, out.Body.BlockWarnings = d.Code, r.Code, warns
	who := roles.Name(me.Role)
	if who == "" {
		who = "会话"
	}
	out.Body.Message = fmt.Sprintf("%s 已留在 %s 下（署名：%s %s），用户和其他会话在需求时间线、req_get 里看得到；订阅了 %s 的会话（含关联它的）会收到一句只告知的提醒，要别人立刻来看就用 ks team tell 发一句「%s 有新留言 %s」。", d.Code, r.Code, who, me.Key, r.Code, r.Code, d.Code)
	if b.SessionClaimed {
		out.Body.Message += "（自动识别不到当前会话，已按 --session " + me.Key + " 署名）"
	}
	out.Body.Hint = writing.CommentHint(text)
	return out, nil
}
