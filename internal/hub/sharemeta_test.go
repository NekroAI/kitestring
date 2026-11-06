package hub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// 分享链接预览（R-286）：没设密码的有效分享露名称和概况，其余一律通用宣传摘要。
func TestShareMeta(t *testing.T) {
	e := newShareEnv(t)
	web := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>纸鸢 Kitestring</title><meta name="description" content="x" /></head><body></body></html>`))
	})
	srv := httptest.NewServer(e.h.Handler(web))
	defer srv.Close()
	get := func(path string) string {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}
	plain := e.createShare(map[string]any{"name": `进展<"x">`, "links": []string{"甲"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	pw := e.createShare(map[string]any{"name": "机密分享", "password": "kite-0821", "links": []string{"乙"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	gone := e.createShare(map[string]any{"name": "撤回的分享", "links": []string{"丙"}, "items": []map[string]any{{"kind": "req", "ref": e.ra.Code}}})
	e.do(e.owner, "POST", "/api/shares/"+gone.ID+"/revoke", nil, nil, 200)

	page := get("/s/" + linkByName(plain, "甲").Token)
	for _, want := range []string{`og:title" content="进展&lt;&#34;x&#34;&gt;"`, "共 1 条需求，已完成 0 条，最近更新 ", `og:image" content="` + srv.URL + `/apple-touch-icon.png"`, `twitter:card`, `og:site_name`} {
		if !strings.Contains(page, want) {
			t.Errorf("无密码分享缺 %s：\n%s", want, page)
		}
	}
	for name, token := range map[string]string{"密码": linkByName(pw, "乙").Token, "撤回": linkByName(gone, "丙").Token, "无效": "kss_nope"} {
		p := get("/s/" + token)
		if !strings.Contains(p, ogPromoTitle) || !strings.Contains(p, "AGPL-3.0") || strings.Contains(p, "机密分享") || strings.Contains(p, "撤回的分享") {
			t.Errorf("%s应显示通用摘要：\n%s", name, p)
		}
	}
	// 到期
	if _, err := e.st.DB().Exec(`UPDATE shares SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), plain.ID); err != nil {
		t.Fatal(err)
	}
	if p := get("/s/" + linkByName(plain, "甲").Token); !strings.Contains(p, ogPromoTitle) || strings.Contains(p, "进展") {
		t.Errorf("到期应显示通用摘要：\n%s", p)
	}
}
