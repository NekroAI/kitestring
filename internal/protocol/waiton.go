package protocol

import (
	"fmt"
	"slices"
	"strings"
)

// ks wait 可订阅的项目事件（R-164）：别的会话提决策、交验收、设为受阻、交阶段小结时，订阅了的会话被唤醒并收到一句说明。
// 只是告诉，不是派活；不进用户收件箱、不推送。
const (
	OnDecision = "decision" // 有会话向用户提了决策
	OnReview   = "review"   // 需求进入待验收
	OnBlocked  = "blocked"  // 需求被设为受阻
	OnStage    = "stage"    // 计划阶段交了小结
	OnSettings = "settings" // 用户在网页上改了项目设置（放行开关、自动派发、授权窗口）（R-171）
	OnStatus   = "status"   // 用户在网页上改了需求状态（R-171）
	OnNone     = "none"     // 不订阅
	OnDefault  = "default"  // 按身份的默认订阅
)

// WaitOnKinds 是全部可订阅的事件，按展示顺序。
var WaitOnKinds = []string{OnDecision, OnReview, OnBlocked, OnStage, OnSettings, OnStatus}

// ParseWaitOn 规整 --on 的值：去重、按固定顺序排列；"none" / "default" 只能单独使用。
func ParseWaitOn(s string) (string, error) {
	var picked []string
	special := ""
	for _, p := range strings.Split(s, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		switch {
		case p == "":
		case p == OnNone || p == OnDefault:
			special = p
		case slices.Contains(WaitOnKinds, p):
			if !slices.Contains(picked, p) {
				picked = append(picked, p)
			}
		default:
			return "", fmt.Errorf("--on 不认识 %q：可选 %s，或 none（不订阅）、default（按身份默认）", p, strings.Join(WaitOnKinds, ","))
		}
	}
	if special != "" {
		if len(picked) > 0 {
			return "", fmt.Errorf("--on %s 只能单独使用", special)
		}
		return special, nil
	}
	if len(picked) == 0 {
		return "", fmt.Errorf("--on 至少写一种事件：%s", strings.Join(WaitOnKinds, ","))
	}
	out := []string{}
	for _, k := range WaitOnKinds {
		if slices.Contains(picked, k) {
			out = append(out, k)
		}
	}
	return strings.Join(out, ","), nil
}
