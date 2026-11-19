package daemon

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// 资源库推送（R-351）：CLI / MCP 把一批素材的本机路径交给 daemon（POST /assets/push）。
// daemon 先把文件按 sha256 复制进本机暂存区（asset-spool/<sha256>，同样内容只存一份）、把这一批记进 asset_jobs，
// 再向 Hub 查哪些文件已有、只传缺的，最后提交。Hub 连不上时这一批留在队列里，连上后自动补传；
// 补传重放用同一个 push_id，Hub 按它去重，不会重复记版本。
// Hub 拒收（GLB 超限、文件类型不对、项目找不到）的不再重试：当场推送时直接报错并丢弃，补传时标为 failed（ks asset queue 能看到原因，7 天后清掉）。
// 附件目录不可用（503）：当场推送时报错不排队（R-353：拒收并说明）；补传时留在队列里等存储恢复。

const (
	assetQueued = "queued"
	assetFailed = "failed"
)

func spoolDir() string { return filepath.Join(Home(), "asset-spool") }

// assetErr 是推送失败：status 是 Hub 的 HTTP 状态（0 = 没连上），transient 表示可以稍后补传。
type assetErr struct {
	status    int
	msg       string
	transient bool
}

func (e *assetErr) Error() string { return e.msg }

// spoolFile 把文件复制进暂存区，返回 sha256 与大小。
func spoolFile(path string) (string, int64, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("读不到文件 %s：%w", path, err)
	}
	defer src.Close()
	if st, err := src.Stat(); err != nil || st.IsDir() {
		return "", 0, fmt.Errorf("不是文件：%s", path)
	}
	dir := spoolDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(dir, "in-*")
	if err != nil {
		return "", 0, err
	}
	defer os.Remove(tmp.Name())
	sum := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, sum), src)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, fmt.Errorf("复制 %s 失败：%w", path, err)
	}
	sha := hex.EncodeToString(sum.Sum(nil))
	dst := filepath.Join(dir, sha)
	if _, err := os.Stat(dst); err == nil {
		return sha, size, nil
	}
	return sha, size, os.Rename(tmp.Name(), dst)
}

// jobFiles 返回一批素材里的全部文件（指向 payload 里的元素，可就地改）。
func jobFiles(p *protocol.AssetPush) []*protocol.AssetFile {
	var out []*protocol.AssetFile
	for i := range p.Items {
		it := &p.Items[i]
		if it.File != nil {
			out = append(out, it.File)
		}
		for j := range it.Previews {
			out = append(out, &it.Previews[j])
		}
	}
	return out
}

func (q *queue) assetJobAdd(p protocol.AssetPush) error {
	b, _ := json.Marshal(p)
	_, err := q.db.Exec(`INSERT INTO asset_jobs(id, payload, state, created_at) VALUES(?, ?, ?, ?)`, p.PushID, string(b), assetQueued, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (q *queue) assetJob(id string) (protocol.AssetPush, error) {
	var p protocol.AssetPush
	var raw string
	if err := q.db.QueryRow(`SELECT payload FROM asset_jobs WHERE id = ?`, id).Scan(&raw); err != nil {
		return p, err
	}
	return p, json.Unmarshal([]byte(raw), &p)
}

func (q *queue) assetJobsQueued() []string {
	rows, err := q.db.Query(`SELECT id FROM asset_jobs WHERE state = ? ORDER BY id`, assetQueued)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

func (q *queue) assetJobNote(id, state, msg string) {
	_, _ = q.db.Exec(`UPDATE asset_jobs SET state = ?, error = ?, tries = tries + 1 WHERE id = ?`, state, msg, id)
}

// assetJobDone 删掉一批，并清掉不再被别的批次用到的暂存文件。
func (q *queue) assetJobDone(id string) {
	p, err := q.assetJob(id)
	_, _ = q.db.Exec(`DELETE FROM asset_jobs WHERE id = ?`, id)
	if err != nil {
		return
	}
	for _, f := range jobFiles(&p) {
		var n int
		_ = q.db.QueryRow(`SELECT COUNT(*) FROM asset_jobs WHERE instr(payload, ?) > 0`, f.SHA256).Scan(&n)
		if n == 0 && f.SHA256 != "" {
			_ = os.Remove(filepath.Join(spoolDir(), f.SHA256))
		}
	}
}

func (q *queue) assetJobsList() []protocol.AssetJob {
	rows, err := q.db.Query(`SELECT id, payload, state, error, tries, created_at FROM asset_jobs ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []protocol.AssetJob{}
	for rows.Next() {
		var j protocol.AssetJob
		var raw string
		if rows.Scan(&j.ID, &raw, &j.State, &j.Error, &j.Tries, &j.CreatedAt) != nil {
			continue
		}
		var p protocol.AssetPush
		_ = json.Unmarshal([]byte(raw), &p)
		j.Batch, j.Items = p.Batch, len(p.Items)
		out = append(out, j)
	}
	return out
}

func (q *queue) assetJobsExpire(now time.Time) {
	rows, err := q.db.Query(`SELECT id FROM asset_jobs WHERE state = ? AND created_at < ?`, assetFailed, now.Add(-7*24*time.Hour).UTC().Format(time.RFC3339))
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		q.assetJobDone(id)
	}
}

func writeProblem(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": code, "detail": msg})
}

// handleAssetPush 收下一批素材：复制进暂存区、记进队列；连着 Hub 时当场推送，连不上时排队。
func (d *Daemon) handleAssetPush(w http.ResponseWriter, r *http.Request) {
	var p protocol.AssetPush
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&p); err != nil {
		writeProblem(w, http.StatusBadRequest, "推送内容无法解析："+err.Error())
		return
	}
	if strings.TrimSpace(p.Batch) == "" {
		writeProblem(w, http.StatusBadRequest, "要写批次（--batch），例如 G11e1")
		return
	}
	if len(p.Items) == 0 {
		writeProblem(w, http.StatusBadRequest, "这一批没有素材")
		return
	}
	p.PushID = eventID()
	for _, f := range jobFiles(&p) {
		if f.Path == "" {
			writeProblem(w, http.StatusBadRequest, "素材文件缺本机路径："+f.Name)
			return
		}
		sha, size, err := spoolFile(f.Path)
		if err != nil {
			writeProblem(w, http.StatusBadRequest, err.Error())
			return
		}
		if f.Name == "" {
			f.Name = filepath.Base(f.Path)
		}
		f.SHA256, f.Size, f.Path, f.ID = sha, size, "", ""
	}
	if err := d.q.assetJobAdd(p); err != nil {
		writeProblem(w, http.StatusInternalServerError, err.Error())
		return
	}
	reply := protocol.AssetPushReply{JobID: p.PushID}
	d.mu.Lock()
	connected := d.connected
	d.mu.Unlock()
	if !connected {
		reply.Queued, reply.Reason = true, "未连接 Hub"
	} else {
		res, err := d.sendAssetJob(r.Context(), p.PushID)
		var ae *assetErr
		switch {
		case err == nil:
			reply.Result = res
		case errors.As(err, &ae) && ae.transient && ae.status != http.StatusServiceUnavailable:
			reply.Queued, reply.Reason = true, ae.msg
		default:
			d.q.assetJobDone(p.PushID)
			code := http.StatusBadGateway
			if errors.As(err, &ae) && ae.status != 0 {
				code = ae.status
			}
			writeProblem(w, code, err.Error())
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func (d *Daemon) handleAssetQueue(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"jobs": d.q.assetJobsList()})
}

// kickAssets 让补传马上跑一轮（连上 Hub 时调用）。
func (d *Daemon) kickAssets() {
	select {
	case d.assetKick <- struct{}{}:
	default:
	}
}

// watchAssets 在连着 Hub 时补传队列里的素材：连上时立刻一轮，之后每 30 秒一轮。
func (d *Daemon) watchAssets(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-d.assetKick:
		}
		d.q.assetJobsExpire(time.Now())
		for _, id := range d.q.assetJobsQueued() {
			d.mu.Lock()
			connected := d.connected
			d.mu.Unlock()
			if !connected {
				break
			}
			res, err := d.sendAssetJob(ctx, id)
			var ae *assetErr
			switch {
			case err == nil:
				d.log.Info("素材已补传", "batch", res.Batch, "items", len(res.Items))
			case errors.As(err, &ae) && ae.transient:
				d.q.assetJobNote(id, assetQueued, ae.msg)
				d.log.Warn("素材补传未成功，稍后重试", "job", id, "err", ae.msg)
			default:
				if !errors.Is(err, sql.ErrNoRows) {
					d.q.assetJobNote(id, assetFailed, err.Error())
					d.log.Warn("素材被 Hub 拒收", "job", id, "err", err)
				}
			}
			if err != nil && errors.As(err, &ae) && ae.transient {
				break // Hub 还不稳，这一轮先停
			}
		}
	}
}

// sendAssetJob 推送队列里的一批：查已有、传缺的、提交；成功后从队列删掉。同一时间只推一批。
func (d *Daemon) sendAssetJob(ctx context.Context, id string) (*protocol.AssetPushResult, error) {
	d.assetMu.Lock()
	defer d.assetMu.Unlock()
	p, err := d.q.assetJob(id)
	if err != nil {
		return nil, err // 已被别的一轮推完
	}
	files := jobFiles(&p)
	var shas []string
	for _, f := range files {
		shas = append(shas, f.SHA256)
	}
	var have struct {
		Files map[string]struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if err := d.hubJSON(ctx, "/api/agent/assets/have", map[string]any{"sha256": shas}, &have); err != nil {
		return nil, err
	}
	reused, uploaded := 0, map[string]string{}
	for _, f := range files {
		if h, ok := have.Files[f.SHA256]; ok {
			f.ID = h.ID
			reused++
			continue
		}
		if id, ok := uploaded[f.SHA256]; ok {
			f.ID = id
			continue
		}
		var ref struct {
			ID     string `json:"id"`
			Reused bool   `json:"reused"`
		}
		if err := d.hubUpload(ctx, filepath.Join(spoolDir(), f.SHA256), f.Name, &ref); err != nil {
			return nil, err
		}
		if ref.Reused {
			reused++
		}
		f.ID, uploaded[f.SHA256] = ref.ID, ref.ID
	}
	var res protocol.AssetPushResult
	if err := d.hubJSON(ctx, "/api/agent/assets/push", p, &res); err != nil {
		return nil, err
	}
	res.Reused = reused
	d.q.assetJobDone(id)
	return &res, nil
}

func (d *Daemon) hubURL(path string) string { return strings.TrimSuffix(d.cfg.HubURL, "/") + path }

func (d *Daemon) hubJSON(ctx context.Context, path string, in, out any) error {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.hubURL(path), strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return d.hubDo(req, out)
}

func (d *Daemon) hubUpload(ctx context.Context, file, name string, out any) error {
	f, err := os.Open(file)
	if err != nil {
		return fmt.Errorf("暂存的文件丢失：%s", name)
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", name)
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.hubURL("/api/agent/assets/files"), pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return d.hubDo(req, out)
}

// hubDo 发请求给 Hub 并解析结果；连不上、网关错误算可补传，其余非 2xx 带上 Hub 的说明。
func (d *Daemon) hubDo(req *http.Request, out any) error {
	req.Header.Set("Authorization", "Bearer "+d.cfg.Token)
	resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
	if err != nil {
		return &assetErr{msg: "无法连接 Hub：" + err.Error(), transient: true}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var p struct {
			Detail string `json:"detail"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &p) == nil && p.Detail != "" {
			msg = p.Detail
		}
		if msg == "" {
			msg = resp.Status
		}
		transient := resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusGatewayTimeout || resp.StatusCode == http.StatusServiceUnavailable
		return &assetErr{status: resp.StatusCode, msg: msg, transient: transient}
	}
	return json.Unmarshal(raw, out)
}
