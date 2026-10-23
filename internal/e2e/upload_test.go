package e2e

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
)

// 附件大小上限可在设置里调，iPhone 录屏（.mov）能上传（R-72）。
func TestUploadLimits(t *testing.T) {
	e := setup(t)
	post := func(name string, data []byte) (int, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		part, _ := mw.CreateFormFile("file", name)
		part.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/files", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		resp, err := e.web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	var cur map[string]any
	e.call("GET", "/api/settings", nil, &cur)
	if cur["upload_image_mb"] != float64(10) || cur["upload_video_mb"] != float64(50) {
		t.Fatalf("默认上限应为 10 / 50 MB：%v / %v", cur["upload_image_mb"], cur["upload_video_mb"])
	}

	// iPhone 录屏：ftyp 品牌是 qt
	mov := append([]byte{0, 0, 0, 0x14, 'f', 't', 'y', 'p', 'q', 't', ' ', ' ', 0, 0, 0, 0, 'q', 't', ' ', ' '}, make([]byte, 2<<20)...)
	if code, body := post("rec.mov", mov); code != 200 || !strings.Contains(body, "video/quicktime") {
		t.Fatalf("MOV 录屏应能上传：%d %s", code, body)
	}

	// 调小录屏上限后，2 MB 的录屏被拒绝，提示里带新的上限
	cur["upload_video_mb"] = 1
	delete(cur, "notify_events")
	delete(cur, "summary_api_key_set")
	e.call("PUT", "/api/settings", cur, &cur)
	if cur["upload_video_mb"] != float64(1) {
		t.Fatalf("录屏上限没保存：%v", cur["upload_video_mb"])
	}
	if code, body := post("rec.mov", mov); code != http.StatusRequestEntityTooLarge || !strings.Contains(body, "1 MB") {
		t.Fatalf("超过上限应被拒绝：%d %s", code, body)
	}
	// 图片上限不受影响
	if code, body := post("a.png", tinyPNG); code != 200 {
		t.Fatalf("图片应照常上传：%d %s", code, body)
	}
}
