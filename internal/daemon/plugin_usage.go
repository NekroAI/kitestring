package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// handleUsage 接收插件直接上报的 token 用量（目前是 DSH）：没有 transcript 可读，
// 由 Host 插件在每次模型回复后把绝对值发过来。只接受 dsh: 会话，按分钟累加进
// usage_minutes，随原有批次发给 Hub（R-206 的同一张表与展示）。
func (d *Daemon) handleUsage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SessionKey string `json:"session_key"`
		AgentID    string `json:"agent_id"` // 子代理（DSH 子会话 ID）；主线程为空（R-251）
		Model      string `json:"model"`
		At         int64  `json:"at"` // Unix 毫秒
		Input      int64  `json:"input"`
		CacheRead  int64  `json:"cache_read"`
		CacheWrite int64  `json:"cache_write"`
		Output     int64  `json:"output"`
		Requests   int64  `json:"requests"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !strings.HasPrefix(in.SessionKey, "dsh:") || in.Model == "" {
		http.Error(w, "用量上报需要 dsh: 会话和 model", http.StatusBadRequest)
		return
	}
	var at time.Time
	if in.At > 0 {
		at = time.UnixMilli(in.At)
	}
	if d.usage != nil {
		if err := d.usage.record(in.SessionKey, in.AgentID, in.Model, at, in.Input, in.CacheWrite, in.CacheRead, in.Output, in.Requests); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	d.wake()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte("{}"))
}
