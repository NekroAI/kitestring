package assetpush

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/audiometa"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// R-381：音效素材按同一套 id、变体推送。WAV 的时长、声道、响度从文件读；OGG 不解码时响度读不出，用 asset.json 的 audio 声明补上（标为声明），
// 声明和文件读出的不一致时提示、按文件；音效可挂预览图当封面；不认识的文件、坏文件报错。
func TestBuildAudio(t *testing.T) {
	dir := fixture(t, `{"namespace": "sfx", "assets": [
	    {"id": "ui.click", "category": "ui", "file": "click.wav", "license": "CC0", "audio": {"channels": 1, "loudness_lufs": -30}},
	    {"id": "hit", "category": "combat", "license": "自制",
	     "variants": {"default": {"file": "hit_01.ogg", "audio": {"loudness_lufs": -14.5, "peak_dbfs": -0.8}}, "b": {"file": "hit_02.wav"}, "c": {"files": {"sound": "hit_03.opus"}}},
	     "audio": {"loudness_lufs": -14.5, "peak_dbfs": -0.8},
	     "previews": ["hit__cover.png"]},
	    {"id": "broken", "category": "ui", "file": "broken.wav"},
	    {"id": "readme", "category": "ui", "file": "notes.txt"}
	  ]}`, "hit__cover.png", "notes.txt")
	put := func(name string, b []byte) {
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("click.wav", audiometa.SineWAV(48000, 2, 0.25, 1000, 0.1, 16))
	put("hit_01.ogg", audiometa.FakeOgg("vorbis", 2, 44100, 0, 22050))
	put("hit_02.wav", audiometa.SineWAV(44100, 1, 0.5, 440, 0.5, 24))
	put("hit_03.opus", audiometa.FakeOgg("opus", 2, 48000, 312, 24000+312))
	put("broken.wav", []byte("RIFF\x00\x00\x00\x00WAVEjunk"))
	p, rep, err := BuildByID(IDOptions{Paths: []string{filepath.Join(dir, "asset.json")}, SkipInvalid: true, NoFFmpeg: true})
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]protocol.AssetItem{}
	for _, it := range p.Items {
		by[it.Key] = it
	}
	checks := map[string]Check{}
	for _, c := range rep.Items {
		checks[c.ID] = c
	}
	click := by["sfx:ui.click"]
	if click.Kind != protocol.AssetAudio || click.Audio == nil || click.Audio.Channels != 2 || click.Audio.Duration != 0.25 || click.Audio.Loudness == nil || len(click.Audio.Declared) != 0 || len(click.Audio.Wave) != audiometa.WaveBins {
		t.Fatalf("WAV 从文件读：%+v %+v", click, click.Audio)
	}
	w := strings.Join(checks["sfx:ui.click"].Warnings, "；")
	if !strings.Contains(w, "audio.channels") || !strings.Contains(w, "audio.loudness_lufs") {
		t.Fatalf("声明和文件不一致要提示：%s", w)
	}
	if !strings.Contains(checks["sfx:ui.click"].Audio, "立体声") || !strings.Contains(ReportText(rep), "音效 · ") {
		t.Fatalf("报告写出音效信息：%s", ReportText(rep))
	}
	hit := by["sfx:hit"]
	if hit.Kind != protocol.AssetAudio || hit.Variant != "default" || hit.Audio.Codec != "vorbis" || hit.Audio.Duration != 0.5 || *hit.Audio.Loudness != -14.5 || *hit.Audio.Peak != -0.8 ||
		!slices.Equal(hit.Audio.Declared, []string{"loudness_lufs", "peak_dbfs"}) || len(hit.Previews) != 1 {
		t.Fatalf("OGG 不解码：时长读文件头，响度、峰值取声明，可挂封面：%+v %+v", hit, hit.Audio)
	}
	if !strings.Contains(checks["sfx:hit"].Audio, "-14.5 LUFS（声明）") {
		t.Fatalf("声明的要标出来：%s", checks["sfx:hit"].Audio)
	}
	b, c := by["sfx:hit@b"], by["sfx:hit@c"]
	if b.Kind != protocol.AssetAudio || b.Parent != "sfx:hit" || b.Audio.Channels != 1 || b.Audio.Loudness == nil || len(b.Audio.Declared) != 0 || b.License != "自制" {
		t.Fatalf("WAV 变体：%+v %+v", b, b.Audio)
	}
	if c.Kind != protocol.AssetAudio || c.Audio.Codec != "opus" || c.Audio.Duration != 0.5 || c.Audio.Loudness != nil {
		t.Fatalf("Opus 变体（files 的 sound）：%+v %+v", c, c.Audio)
	}
	if !strings.Contains(strings.Join(checks["sfx:hit@c"].Warnings, ""), "读不出响度") {
		t.Fatalf("OGG 读不出响度要提示：%v", checks["sfx:hit@c"].Warnings)
	}
	if e := strings.Join(checks["sfx:broken"].Errors, ""); !strings.Contains(e, "读不出来") {
		t.Fatalf("坏的 WAV 要报错：%v", checks["sfx:broken"].Errors)
	}
	if e := strings.Join(checks["sfx:readme"].Errors, ""); !strings.Contains(e, "音效 OGG / WAV") {
		t.Fatalf("不支持的类型：%v", checks["sfx:readme"].Errors)
	}
	if _, ok := by["sfx:broken"]; ok {
		t.Fatal("有错的不推")
	}
	// 项目字段里不再出现 audio（纸鸢认识的通用字段）
	if strings.Contains(string(click.Meta), "audio") {
		t.Fatalf("audio 不进项目字段：%s", click.Meta)
	}
}

func TestAudioDeclBad(t *testing.T) {
	dir := fixture(t, `{"id": "sfx:x", "category": "ui", "file": "x.wav", "audio": {"loudness_lufs": 40}}`)
	if err := os.WriteFile(filepath.Join(dir, "x.wav"), audiometa.SineWAV(8000, 1, 0.1, 440, 0.2, 16), 0o600); err != nil {
		t.Fatal(err)
	}
	_, rep, err := BuildByID(IDOptions{Paths: []string{dir}, NoFFmpeg: true})
	if err != nil || rep.Bad() != 1 || !strings.Contains(strings.Join(rep.Items[0].Errors, ""), "audio.loudness_lufs") {
		t.Fatalf("不合理的声明要报错：%+v %v", rep.Items, err)
	}
}
