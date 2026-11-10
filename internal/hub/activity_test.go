package hub

import (
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/store"
)

// 最近动态接口（R-296）：会话交证据、留言这些动作经 /api/activity 出来，带主语；mine 只留你做的。
func TestActivityAPI(t *testing.T) {
	e := newShareEnv(t)
	if _, err := e.st.AddEvidence(e.ctx, store.Evidence{RequirementID: e.ra.ID, SessionKey: e.dev1, Kind: "text", Summary: "接口做完"}); err != nil {
		t.Fatal(err)
	}
	tick() // Windows 的时钟按 tick 走：连着写入的时间可能相同，排序就不定
	if _, err := e.st.CreateComment(e.ctx, e.rb, store.Comment{By: e.pm, Role: "pm", Text: "先别动"}); err != nil {
		t.Fatal(err)
	}
	tick()
	if _, err := e.st.CreateDecision(e.ctx, store.Decision{Kind: store.KindNote, RequirementID: e.ra.ID, Question: e.ra.Title, Answer: "看下手机"}); err != nil {
		t.Fatal(err)
	}
	var out struct{ Items []store.Activity }
	e.do(e.owner, "GET", "/api/activity?limit=10", nil, &out, 200)
	if len(out.Items) != 3 || out.Items[0].Kind != store.FeedNote || out.Items[0].Actor != store.ActorUser ||
		out.Items[1].Kind != store.FeedComment || out.Items[1].Role != "pm" || out.Items[1].SessionTitle != "排期" || out.Items[1].Code != e.rb.Code ||
		out.Items[2].Kind != store.FeedEvidence || out.Items[2].Role != "dev" || out.Items[2].SessionTitle != "分享后端" {
		t.Fatalf("动态不对：%+v", out.Items)
	}
	e.do(e.owner, "GET", "/api/activity?mine=true", nil, &out, 200)
	if len(out.Items) != 1 || out.Items[0].Kind != store.FeedNote {
		t.Fatalf("只看我的不对：%+v", out.Items)
	}
}

// tick 等到时钟走过一格（Windows 上最长 15.6 毫秒）。
func tick() { time.Sleep(20 * time.Millisecond) }
