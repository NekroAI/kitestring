package hub

import (
	"context"
	"fmt"

	"github.com/NekroAI/kitestring/internal/roles"
	"github.com/NekroAI/kitestring/internal/store"
)

// 主助理（R-190）：每项目唯一、常驻，替用户判断。看证据写验收建议、代答（R-163）、离开期间小结（R-165）、放行草稿（R-166，与主调度共有）
// 从顾问移到主助理；顾问只答问题、做设计，不常驻、不订阅事件。项目没有主助理时不退回给顾问：代答、放行（主调度仍可放行）直接不可用，
// 事情照旧进用户的「要你处理」，验收建议和小结就不写。
//
// 过渡迁移做在系统里，不靠会话自己记得：
//   - 授权窗口跟着主助理走：绑在非主助理会话上的生效窗口（老的顾问窗口、卸任的主助理）直接失效，被授权的会话和订阅了 settings 的会话收到说明，
//     设置里的授权面板提示给主助理重新开。Hub 启动时清一次存量，之后在接任 / 卸任主助理时清。
//   - 项目还没有主助理、却有顾问会话持有授权窗口或挂着 ks wait 常驻时，给那个顾问会话发一次提示（团队消息，标题 aideHintQ）；
//     按标题查重，同一会话只发一次，Hub 重启后也不重复。

const aideHintQ = "身份调整：代答等已移到主助理"

const aideHintText = "身份调整（R-190）：代答、放行、验收建议、离开期间小结已移到主助理；本项目还没有主助理，要接手请运行 ks role aide。顾问只答问题、做设计，不常驻、不订阅事件。"

// liveAide 返回项目里在岗（没结束、没空等下线）的主助理会话键，没有时为空。
func (h *Hub) liveAide(ctx context.Context, projectID string) string {
	if projectID == "" {
		return ""
	}
	list, err := h.store.RoleSessions(ctx, projectID)
	if err != nil {
		return ""
	}
	for _, x := range list {
		if x.Role == roles.Aide && seatHolder(x) && x.OfflineAt == nil { // 空等下线的主助理不在岗（R-272）
			return x.Key
		}
	}
	return ""
}

// aideHint：顾问会话 x 持有授权窗口或常驻（挂着 ks wait）、项目还没有主助理时，提示它一次。
func (h *Hub) aideHint(ctx context.Context, x store.Session, holdsGrant bool) {
	if x.Role != roles.Advisor || x.ProjectID == "" || x.DoneAt != nil || x.State == store.StateEnded || x.State == store.StateLost {
		return
	}
	if !holdsGrant && !x.Parked {
		return
	}
	if h.liveAide(ctx, x.ProjectID) != "" || h.store.HasTell(ctx, x.Key, aideHintQ) {
		return
	}
	d, err := h.store.CreateDecision(ctx, store.Decision{Kind: store.KindTell, SessionKey: x.Key, ProjectID: x.ProjectID, Question: aideHintQ, Answer: aideHintText})
	if err != nil {
		h.log.Warn("发身份调整提示失败", "err", err, "to", x.Key)
		return
	}
	h.decisionsChanged(ctx, d)
}

// revokeGrantsOf 让绑在会话 key 上的生效窗口失效（R-190：授权跟着主助理走），告诉它和订阅了 settings 的会话。
func (h *Hub) revokeGrantsOf(ctx context.Context, key, why string) {
	x, err := h.store.GetSession(ctx, key)
	if err != nil || x.ProjectID == "" {
		return
	}
	g, err := h.store.ActiveGrant(ctx, x.ProjectID)
	if err != nil || g.SessionKey != key {
		return
	}
	if g, err = h.store.RevokeGrant(ctx, g.ID); err != nil {
		h.log.Warn("让授权窗口失效失败", "err", err, "grant", g.Code)
		return
	}
	h.tellGrant(ctx, g, fmt.Sprintf("授权窗口 %s 已失效：%s，代答只由本项目主助理做。要继续代答，请用户在设置里给主助理重新开。", g.Code, why))
	h.onSettingsChanged(ctx, g.ProjectID, fmt.Sprintf("授权窗口 %s（授权给 %s）已失效：%s；代答只由主助理做，需要时请用户给主助理重新开。", g.Code, h.actorName(ctx, g.SessionKey), why), g.SessionKey)
	h.broker.publish("grants", "decisions")
}

// MigrateAide 供测试模拟 Hub 启动时的迁移。
func (h *Hub) MigrateAide(ctx context.Context) { h.migrateAide(ctx) }

// migrateAide：Hub 启动时做一次身份调整迁移——持有窗口或常驻的顾问提示一次，绑在非主助理会话上的生效窗口失效。
func (h *Hub) migrateAide(ctx context.Context) {
	advs, err := h.store.SessionsWithRole(ctx, roles.Advisor)
	if err != nil {
		h.log.Warn("身份调整迁移：查顾问会话失败", "err", err)
		return
	}
	for _, x := range advs {
		g, gerr := h.store.ActiveGrant(ctx, x.ProjectID)
		h.aideHint(ctx, x, gerr == nil && g.SessionKey == x.Key)
	}
	list, err := h.store.ListGrants(ctx, "", 50)
	if err != nil {
		return
	}
	for _, g := range list {
		if g.Status != store.GrantActive {
			continue
		}
		if x, err := h.store.GetSession(ctx, g.SessionKey); err == nil && x.Role == roles.Aide {
			continue
		}
		h.revokeGrantsOf(ctx, g.SessionKey, "代答已移到主助理，这个窗口授权给的不是主助理")
	}
}
