package store

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-381：音效素材存下音频信息，变体各有版本；同一文件重推不新增版本，asset.json 声明的值变了才记新版本；
// 单件审核和批量审核（按 kind 筛选）与其它素材相同。
func TestPushAudioAssets(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	f1, _ := s.CreateFile(ctx, "hit_01.ogg", "audio/ogg", "h1", 10, "device:x")
	f2, _ := s.CreateFile(ctx, "hit_02.wav", "audio/wav", "h2", 10, "device:x")
	lufs := func(v float64) *float64 { return &v }
	item := func(loud float64) []protocol.AssetItem {
		return []protocol.AssetItem{
			{Key: "sfx:hit", Kind: protocol.AssetAudio, Category: "combat", Variant: "default", File: &protocol.AssetFile{ID: f1.ID},
				Audio: &protocol.AssetAudioInfo{Codec: "vorbis", Duration: 0.5, Channels: 2, SampleRate: 44100, Loudness: lufs(loud), Declared: []string{"loudness_lufs"}}},
			{Key: "sfx:hit@b", Parent: "sfx:hit", Variant: "b", Kind: protocol.AssetAudio, Category: "combat", File: &protocol.AssetFile{ID: f2.ID},
				Audio: &protocol.AssetAudioInfo{Codec: "pcm", Duration: 0.25, Channels: 1, SampleRate: 22050, Loudness: lufs(-12), Peak: lufs(-1), Wave: []int{0, 50, 100}}},
		}
	}
	push := func(items []protocol.AssetItem) protocol.AssetPushResult {
		t.Helper()
		r, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ByID: true, Items: items})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	push(item(-14))
	d, err := s.FindAsset(ctx, "p1", "sfx:hit")
	if err != nil || d.Kind != protocol.AssetAudio || d.Latest.Audio == nil || *d.Latest.Audio.Loudness != -14 || !slices.Equal(d.Latest.Audio.Declared, []string{"loudness_lufs"}) || len(d.Variants) != 1 {
		t.Fatalf("音效素材：%+v %+v %v", d.Asset, d.Latest, err)
	}
	b, _ := s.FindAsset(ctx, "p1", "sfx:hit@b")
	if a := b.Latest.Audio; a == nil || a.Channels != 1 || *a.Peak != -1 || !slices.Equal(a.Wave, []int{0, 50, 100}) || b.Parent == nil {
		t.Fatalf("变体的音频信息：%+v", b.Latest.Audio)
	}
	// 同一文件、同样的声明：不新增版本；声明的响度改了：素材本身记新版本，变体（全从文件读）不变
	if r := push(item(-14)); r.Items[0].Result != "unchanged" || r.Items[1].Result != "unchanged" {
		t.Fatalf("内容没变：%+v", r.Items)
	}
	if r := push(item(-16)); r.Items[0].Result != "updated" || r.Items[1].Result != "unchanged" {
		t.Fatalf("声明变了记新版本：%+v", r.Items)
	}
	// 审核：单件返工、按 kind 批量通过
	if _, _, err := s.ReviewAsset(ctx, NewAssetReview{Asset: b.Code, Verdict: AssetRework, Comment: "尾音太长", Author: AssetAuthor{Kind: SubmitterUser}}); err != nil {
		t.Fatal(err)
	}
	targets, err := s.AssetTargets(ctx, "p1", nil, AssetFilter{Kind: protocol.AssetAudio, Status: AssetPending})
	if err != nil || len(targets) != 1 || targets[0].Code != d.Code {
		t.Fatalf("按 kind 筛选待审的音效：%+v %v", targets, err)
	}
	if _, err := s.ReviewAssetBatch(ctx, NewAssetBatch{Assets: targets, Verdict: AssetApproved, Author: AssetAuthor{Kind: SubmitterUser}}); err != nil {
		t.Fatal(err)
	}
	d, _ = s.GetAsset(ctx, d.Code)
	b, _ = s.GetAsset(ctx, b.Code)
	if d.Status != AssetApproved || b.Status != AssetRework || len(b.Reviews) != 1 {
		t.Fatalf("审核后：%s %s", d.Status, b.Status)
	}
}

// 老库升级：去掉 audio 列后重开，补上列，旧版本读出来没有音频信息，新推的音效照常存。
func TestAudioColumnMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	f, _ := s.CreateFile(ctx, "a.glb", "model/gltf-binary", "a", 10, "device:x")
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", Batch: "G1", Items: []protocol.AssetItem{{Key: "a", Kind: protocol.AssetPart, File: &protocol.AssetFile{ID: f.ID}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE asset_versions DROP COLUMN audio`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d, err := s.GetAsset(ctx, "A-1")
	if err != nil || d.Latest == nil || d.Latest.Audio != nil {
		t.Fatalf("升级后读旧数据：%+v %v", d.Latest, err)
	}
	w, _ := s.CreateFile(ctx, "b.wav", "audio/wav", "b", 10, "device:x")
	if _, err := s.PushAssets(ctx, NewAssetPush{ProjectID: "p1", ByID: true, Items: []protocol.AssetItem{{Key: "sfx:b", Kind: protocol.AssetAudio, Category: "ui", File: &protocol.AssetFile{ID: w.ID}, Audio: &protocol.AssetAudioInfo{Channels: 1}}}}); err != nil {
		t.Fatal(err)
	}
	if b, err := s.FindAsset(ctx, "p1", "sfx:b"); err != nil || b.Latest.Audio == nil || b.Latest.Audio.Channels != 1 {
		t.Fatalf("升级后写新列：%+v %v", b.Latest, err)
	}
}
