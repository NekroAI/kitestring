// Package profile 处理「Claude 配置」（R-240）：用 CLAUDE_CONFIG_DIR 开出来的不同配置目录。
// 只涉及目录路径、显示名和启动命令，不碰任何凭证（D13）：ANTHROPIC_API_KEY / ANTHROPIC_BASE_URL 不读、不存、不上报。
package profile

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Dir 是一个接入的 Claude 配置目录。
type Dir struct {
	Path     string `json:"path"`
	Launcher string `json:"launcher,omitempty"` // 用户开这个配置的启动命令，如 claude-x
}

// DefaultDir 是默认配置目录 ~/.claude。
func DefaultDir(home string) string { return filepath.Join(home, ".claude") }

// Expand 展开 ~ 并清理路径；不是绝对路径时报错。
func Expand(home, p string) (string, error) {
	p = strings.TrimSpace(p)
	switch {
	case p == "~":
		p = home
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("配置目录必须是绝对路径（或以 ~/ 开头）：" + p)
	}
	return filepath.Clean(p), nil
}

// IsDefault 判断目录是否就是默认的 ~/.claude（空串也算默认）。
func IsDefault(home, dir string) bool {
	return dir == "" || filepath.Clean(dir) == DefaultDir(home)
}

// Name 是显示名：目录名去掉开头的点，如 ~/.claude-x → claude-x。
func Name(dir string) string {
	n := strings.TrimLeft(filepath.Base(filepath.Clean(dir)), ".")
	if n == "" {
		return filepath.Base(dir)
	}
	return n
}

// ConfigFile 是目录里存用户级 MCP 注册的文件：默认目录在 ~/.claude.json，其他目录在 <目录>/.claude.json。
func ConfigFile(home, dir string) string {
	if IsDefault(home, dir) {
		return filepath.Join(home, ".claude.json")
	}
	return filepath.Join(dir, ".claude.json")
}

// ListFile 是本机记录已接入目录的文件。
func ListFile(home string) string { return filepath.Join(home, ".kitestring", "claude-dirs.json") }

// Load 读本机记录的目录列表；文件不存在返回空。
func Load(home string) ([]Dir, error) {
	b, err := os.ReadFile(ListFile(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Dirs []Dir `json:"dirs"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Dirs, nil
}

// Save 写回目录列表；列表为空时删除文件。
func Save(home string, dirs []Dir) error {
	path := ListFile(home)
	if len(dirs) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	b, _ := json.MarshalIndent(map[string]any{"dirs": dirs}, "", "  ")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

// Merge 把 add 并入 list：同一路径只留一条，新的 launcher 覆盖旧的（新的为空则保留旧的）。
func Merge(list []Dir, add ...Dir) []Dir {
	for _, a := range add {
		found := false
		for i := range list {
			if list[i].Path == a.Path {
				if a.Launcher != "" {
					list[i].Launcher = a.Launcher
				}
				found = true
			}
		}
		if !found {
			list = append(list, a)
		}
	}
	return list
}

// Remove 从列表里去掉指定路径。
func Remove(list []Dir, paths ...string) []Dir {
	var out []Dir
	for _, d := range list {
		drop := false
		for _, p := range paths {
			if d.Path == p {
				drop = true
			}
		}
		if !drop {
			out = append(out, d)
		}
	}
	return out
}

// Info 是 daemon 随事件上报的来源信息；默认目录全为空。
type Info struct {
	Name     string
	Dir      string
	Launcher string
}

// Resolve 由 hook 带来的 CLAUDE_CONFIG_DIR 算出来源信息。
func Resolve(home, configDir string) Info {
	if configDir == "" {
		return Info{}
	}
	dir, err := Expand(home, configDir)
	if err != nil || IsDefault(home, dir) {
		return Info{}
	}
	info := Info{Name: Name(dir), Dir: dir}
	if list, err := Load(home); err == nil {
		for _, d := range list {
			if d.Path == dir {
				info.Launcher = d.Launcher
			}
		}
	}
	return info
}
