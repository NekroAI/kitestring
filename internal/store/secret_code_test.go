package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// R-326：凭据的稳定编号 S-x——新建依次分配、老库按创建先后补上；按编号、ID、名称都能找到，名称对上多条时报出编号。
func TestSecretCode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	a, err := s.CreateSecret(ctx, Secret{Name: "甲"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateSecret(ctx, Secret{Name: "乙"})
	if a.Code != "S-1" || b.Code != "S-2" {
		t.Fatalf("依次分配编号：%s %s", a.Code, b.Code)
	}
	for _, ref := range []string{"S-2", "s-2", " S-2 ", b.ID, "乙"} {
		if x, err := s.GetSecret(ctx, ref); err != nil || x.ID != b.ID {
			t.Fatalf("按 %q 找：%+v %v", ref, x, err)
		}
	}
	if _, err := s.GetSecret(ctx, "S-9"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有的编号：%v", err)
	}
	if _, err := s.CreateSecret(ctx, Secret{Name: "s-5"}); err == nil {
		t.Fatal("名称不能写成编号的样子")
	}
	b.Name = "S-1"
	if _, err := s.SaveSecret(ctx, b); err == nil {
		t.Fatal("改名也不能写成编号的样子")
	}

	// 老数据里同名（兜底）：拒绝并列出编号
	if _, err := s.DB().Exec(`UPDATE secrets SET name = '甲' WHERE id = ?`, b.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.GetSecret(ctx, "甲")
	var amb *AmbiguousSecretError
	if !errors.As(err, &amb) || !strings.Contains(err.Error(), "S-1、S-2") {
		t.Fatalf("名称对上多条应拒绝并列出编号：%v", err)
	}

	// 迁移：老库没有编号时按创建先后补上
	if _, err := s.DB().Exec(`DROP INDEX secrets_seq; UPDATE secrets SET seq = 0`); err != nil {
		t.Fatal(err)
	}
	if err := fillSecretSeq(s.DB()); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.GetSecret(ctx, a.ID); x.Code != "S-1" {
		t.Fatalf("先建的补 S-1：%s", x.Code)
	}
	if x, _ := s.GetSecret(ctx, b.ID); x.Code != "S-2" {
		t.Fatalf("后建的补 S-2：%s", x.Code)
	}
	c, _ := s.CreateSecret(ctx, Secret{Name: "丙"})
	if c.Code != "S-3" {
		t.Fatalf("补完后接着分配：%s", c.Code)
	}
}
