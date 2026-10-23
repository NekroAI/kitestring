package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// 删除设备（R-92）：未吊销不能删；吊销后删除释放名称、从列表消失，历史会话保留。
func TestDeleteDevice(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.CreateDevice(ctx, "mac", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`INSERT INTO sessions(key, tool, session_id, device_id, state, started_at, last_active_at) VALUES('claude:a','claude','a',?, 'idle', ?, ?)`, d.ID, now(), now()); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, d.ID); !errors.Is(err, ErrDeviceActive) {
		t.Fatalf("未吊销应拒绝删除：%v", err)
	}
	if _, err := s.CreateDevice(ctx, "mac", "h2"); err == nil {
		t.Fatal("名称被占用时不应能同名新增")
	}
	if err := s.RevokeDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if devs, _ := s.ListDevices(ctx); len(devs) != 0 {
		t.Fatalf("删除后列表应为空：%+v", devs)
	}
	if _, err := s.CreateDevice(ctx, "mac", "h3"); err != nil {
		t.Fatalf("删除后应可同名新增：%v", err)
	}
	if _, err := s.GetSession(ctx, "claude:a"); err != nil {
		t.Fatalf("历史会话应保留：%v", err)
	}
	if err := s.DeleteDevice(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在应 404：%v", err)
	}
}
