// Package client 是访问本机 daemon unix socket 的 HTTP 客户端，供 ks hook / ks mcp / CLI 使用。
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/daemon"
)

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
