// Package daemon 实现设备常驻进程 ks daemon（D12）：汇聚 hook 事件、离线队列、git 采集、
// 与 Hub 的双向 WebSocket、待注入决策缓存，并为 ks hook / ks mcp / CLI 提供本机 unix socket 接口。
package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Config 是 daemon 配置文件内容。
type Config struct {
	HubURL string `json:"hub_url"` // 例：http://hub.local:7310
	Token  string `json:"token"`
}

// Home 返回 Kitestring 本机目录，可用 KS_HOME 覆盖（测试与多实例用）。
func Home() string {
	if h := os.Getenv("KS_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kitestring")
}

// ConfigPath 返回配置文件路径。
func ConfigPath() string { return filepath.Join(Home(), "config.json") }

// SocketPath 返回 daemon 的 unix socket 路径。
func SocketPath() string { return filepath.Join(Home(), "daemon.sock") }

// LoadConfig 读取配置。
func LoadConfig() (Config, error) {
	var c Config
	b, err := os.ReadFile(ConfigPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, errors.New("尚未配置：请先运行 ks login --hub <地址> --token <设备 token>")
		}
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.HubURL == "" || c.Token == "" {
		return c, errors.New("配置缺少 hub_url 或 token")
	}
	return c, nil
}

// SaveConfig 写入配置（权限 0600，含 token）。
func SaveConfig(c Config) error {
	if err := os.MkdirAll(Home(), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(c, "", "  ")
	return os.WriteFile(ConfigPath(), b, 0o600)
}
