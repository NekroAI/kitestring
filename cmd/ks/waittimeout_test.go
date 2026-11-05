package main

import (
	"testing"
	"time"
)

// R-272：Claude Code 的 ks wait 单次等待默认、也最多 55 分钟；项目设置可调得更短；--timeout 超过上限按上限截断并提示；
// 常驻会话最后一段空等只等到空等下线的时刻。
func TestWaitTimeout(t *testing.T) {
	m := time.Minute
	cases := []struct {
		name            string
		want, max, left time.Duration
		got, limit      time.Duration
		clipped         bool
	}{
		{"不填按 55 分钟", 0, 0, 0, 55 * m, 55 * m, false},
		{"--timeout 3h 截到 55 分钟", 3 * time.Hour, 0, 0, 55 * m, 55 * m, true},
		{"--timeout 12h 截到 55 分钟", 12 * time.Hour, 55 * m, 0, 55 * m, 55 * m, true},
		{"--timeout 20m 照用", 20 * m, 0, 0, 20 * m, 55 * m, false},
		{"项目设成 30 分钟", 0, 30 * m, 0, 30 * m, 30 * m, false},
		{"项目设置超过 55 也按 55", 0, 90 * m, 0, 55 * m, 55 * m, false},
		{"--timeout 超过项目上限", 40 * m, 30 * m, 0, 30 * m, 30 * m, true},
		{"离空等下线只剩 10 分钟", 0, 0, 10 * m, 10 * m, 55 * m, false},
		{"离空等下线还早", 0, 0, 4 * time.Hour, 55 * m, 55 * m, false},
	}
	for _, c := range cases {
		got, limit, clipped := waitTimeout(c.want, c.max, c.left)
		if got != c.got || limit != c.limit || clipped != c.clipped {
			t.Errorf("%s：得到 %v / %v / %v，应为 %v / %v / %v", c.name, got, limit, clipped, c.got, c.limit, c.clipped)
		}
	}
}
