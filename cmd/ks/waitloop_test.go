package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/NekroAI/kitestring/internal/daemon"
)

// R-227：ks wait 断线（daemon 重启、升级）后自动重连，不把 Agent 唤醒。
func TestWaitLoopReconnects(t *testing.T) {
	calls := 0
	res := waitLoop(context.Background(), time.Now().Add(time.Minute), time.Millisecond, func(time.Duration) (daemon.WaitResult, error) {
		calls++
		switch calls {
		case 1:
			return daemon.WaitResult{}, errors.New("无法连接本机 daemon") // 重启中，连不上
		case 2:
			return daemon.WaitResult{Reason: "canceled"}, nil // 旧 daemon 收尾时回的取消，本进程并没被取消
		}
		return daemon.WaitResult{Reason: "delivered", Text: "有新消息"}, nil
	})
	if res.Reason != "delivered" || res.Text != "有新消息" || calls != 3 {
		t.Fatalf("断线应自动重连直到拿到结果：%+v calls=%d", res, calls)
	}
}

func TestWaitLoopStopsOnCancelAndDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	res := waitLoop(ctx, time.Now().Add(time.Minute), time.Millisecond, func(time.Duration) (daemon.WaitResult, error) {
		cancel()
		return daemon.WaitResult{Reason: "canceled"}, nil
	})
	if res.Reason != "canceled" {
		t.Fatalf("本进程被取消应返回 canceled：%+v", res)
	}
	res = waitLoop(context.Background(), time.Now().Add(30*time.Millisecond), time.Millisecond, func(time.Duration) (daemon.WaitResult, error) {
		return daemon.WaitResult{}, errors.New("连不上")
	})
	if res.Reason != "timeout" {
		t.Fatalf("一直连不上，到截止时间应按超时结束：%+v", res)
	}
}
