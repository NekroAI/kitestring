package store

import (
	"context"
	"testing"
	"time"
)

// 凭据时间线（R-318 测试偶发失败）：同一毫秒里记的几条按写入顺序排（新的在前），不靠 ID 里的随机部分。
func TestSecretLogsSameMillisecond(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	x, err := s.CreateSecret(ctx, Secret{Name: "同毫秒", Fields: []SecretField{{Env: "A", Label: "A"}}})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Truncate(time.Millisecond)
	kinds := []string{SecretLogUse, SecretLogPlain, SecretLogSensitive, SecretLogEdit, SecretLogUse, SecretLogRevoke}
	for _, k := range kinds {
		if _, err := s.AddSecretLog(ctx, SecretLog{SecretID: x.ID, Kind: k, At: at, Days: -1}); err != nil {
			t.Fatal(err)
		}
	}
	logs, err := s.ListSecretLogs(ctx, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range logs {
		if want := kinds[len(kinds)-1-i]; l.Kind != want {
			t.Fatalf("第 %d 条应是 %s，实际 %s", i, want, l.Kind)
		}
	}
	if last, err := s.LastSecretUse(ctx, x.ID); err != nil || last.ID != logs[1].ID {
		t.Fatalf("最近一次取用应是后写的那条：%+v %v", last, err)
	}
}
