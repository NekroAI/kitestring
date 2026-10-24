// Package client 是访问本机 daemon unix socket 的 HTTP 客户端，供 ks hook / ks mcp / CLI 使用。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/daemon"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/version"
)

// VersionHeader 带上调用方（ks mcp / CLI）的版本，daemon 据此认出还在跑旧版 MCP 的会话（R-59）。
const VersionHeader = "X-KS-Version"

// Client 访问本机 daemon。
type Client struct {
	http *http.Client
}

// New 创建客户端，timeout 为单次请求总超时。
func New(timeout time.Duration) *Client {
	sock := daemon.SocketPath()
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
		DisableKeepAlives: true,
	}
	return &Client{http: &http.Client{Transport: tr, Timeout: timeout}}
}

// Do 发送请求并把 JSON 响应解到 out（可为 nil）。非 2xx 时返回包含服务端说明的错误。
func (c *Client) Do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://daemon"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(VersionHeader, version.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接本机 Kitestring daemon（%s）：%w", daemon.SocketPath(), err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s", errorText(resp.StatusCode, raw))
	}
	if out != nil && len(raw) > 0 {
		return json.Unmarshal(raw, out)
	}
	return nil
}

// errorText 提取 Huma problem+json 或纯文本错误。
func errorText(code int, raw []byte) string {
	var p struct {
		Detail string `json:"detail"`
		Errors []struct {
			Message  string `json:"message"`
			Location string `json:"location"`
		} `json:"errors"`
	}
	if json.Unmarshal(raw, &p) == nil && p.Detail != "" {
		msg := p.Detail
		for _, e := range p.Errors {
			msg += "；" + e.Location + "：" + e.Message
		}
		return msg
	}
	return fmt.Sprintf("HTTP %d：%s", code, strings.TrimSpace(string(raw)))
}

// Upload 以 multipart 上传本地文件到 path（经 daemon 转发到 Hub），结果解到 out。
func (c *Client) Upload(ctx context.Context, path, file string, out any) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		part, err := mw.CreateFormFile("file", filepath.Base(file))
		if err == nil {
			_, err = io.Copy(part, f)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon"+path, pr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set(VersionHeader, version.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("无法连接本机 Kitestring daemon：%w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s", errorText(resp.StatusCode, raw))
	}
	return json.Unmarshal(raw, out)
}

// Fetch 读取 path 的原始内容（例如附图），最多 limit 字节。
func (c *Client) Fetch(ctx context.Context, path string, limit int64) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon"+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set(VersionHeader, version.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	return b, resp.Header.Get("Content-Type"), err
}

// SaveAttachment 把附件下载到本机（经 daemon 转发），返回本机路径；已下载过的直接返回（R-221）。
// 路径与 daemon 送达决策时下载的一致（daemon.AttachmentPath）。
func (c *Client) SaveAttachment(ctx context.Context, f protocol.File) (string, error) {
	p := daemon.AttachmentPath(f)
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon/agent/files/"+url.PathEscape(f.ID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set(VersionHeader, version.Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), "dl-*")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return p, os.Rename(tmp.Name(), p)
}

// userFileKeys 是需求详情里用户附的文件所在的字段：描述附件、意见 / 答复 / 留言的附件、决策的背景图与选项图。
// 证据、审查意见里的附件是 Agent 自己交的，不下载。
var userFileKeys = map[string]bool{"files": true, "answer_files": true, "context_files": true, "option_files": true}

// LocalizeFiles 把需求详情（解出来的 JSON）里用户附的文件下载到本机，并在每个附件上写上 path（本机路径）；
// 下载失败的写 path_error。供 req_get、ks req get 使用，Agent 据此直接读用户附的文件（R-221）。
func (c *Client) LocalizeFiles(ctx context.Context, v any) {
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, val := range x {
				if k == "evidence" || k == "reviews" {
					continue
				}
				if list, ok := val.([]any); ok && userFileKeys[k] {
					for _, it := range list {
						m, _ := it.(map[string]any)
						id, _ := m["id"].(string)
						if id == "" {
							continue
						}
						name, _ := m["name"].(string)
						mime, _ := m["mime"].(string)
						if p, err := c.SaveAttachment(ctx, protocol.File{ID: id, Name: name, Mime: mime}); err == nil {
							m["path"] = p
						} else {
							m["path_error"] = err.Error()
						}
					}
					continue
				}
				walk(val)
			}
		case []any:
			for _, it := range x {
				walk(it)
			}
		}
	}
	walk(v)
}
