package hub

import (
	"math"
	"strings"
	"testing"

	"github.com/NekroAI/kitestring/internal/audiometa"
	"github.com/NekroAI/kitestring/internal/protocol"
	"github.com/NekroAI/kitestring/internal/store"
)

// R-381：资源库收音效。文件头认 OGG / WAV；推送时主文件要是音效，推送端没给音频信息时 Hub 读文件头补上，给了的做范围检查；
// 分享页能播放（不算下载原文件），访客上传的 WAV 收下为音效的新版本时 Hub 读出时长、响度，类型不符的不收为新版本。
func TestPushAudioHub(t *testing.T) {
	if assetFileMime([]byte("OggS\x00\x02")) != "audio/ogg" || assetFileMime(audiometa.SineWAV(8000, 1, 0.01, 440, 0.1, 16)[:16]) != "audio/wav" || assetFileMime([]byte("ID3\x03")) != "" {
		t.Fatal("文件头认音效")
	}
	e := newShareEnv(t)
	e.pushFixture()
	wav := e.assetFile("hit.wav", "audio/wav", audiometa.SineWAV(48000, 2, 0.5, 1000, 0.1, 16))
	ogg := e.assetFile("hit_b.ogg", "audio/ogg", audiometa.FakeOgg("vorbis", 1, 44100, 0, 44100))
	glb := e.assetFile("x.glb", glbMime, append(append([]byte{}, glbHead...), 'q'))
	bad := -500.0
	res, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", SessionKey: e.dev1, Items: []protocol.AssetItem{
		{Key: "sfx:hit", Kind: protocol.AssetAudio, Category: "音效", File: &wav, Variant: "default"},
		{Key: "sfx:hit@b", Parent: "sfx:hit", Variant: "b", Kind: protocol.AssetAudio, Category: "音效", File: &ogg,
			Audio: &protocol.AssetAudioInfo{Codec: "vorbis", Duration: 1, Channels: 1, SampleRate: 44100, Loudness: &bad, Wave: []int{300, -2, 50}}},
	}})
	if err != nil || len(res.Items) != 2 {
		t.Fatalf("推送音效：%+v %v", res, err)
	}
	d, err := e.st.FindAsset(e.ctx, "p1", "sfx:hit")
	a := d.Latest.Audio
	if err != nil || d.Kind != protocol.AssetAudio || a == nil || a.Channels != 2 || a.Duration != 0.5 || a.Loudness == nil || math.Abs(*a.Loudness+20) > 0.5 || len(a.Wave) != audiometa.WaveBins {
		t.Fatalf("Hub 读文件补音频信息：%+v %+v %v", d.Asset, a, err)
	}
	b, _ := e.st.FindAsset(e.ctx, "p1", "sfx:hit@b")
	if x := b.Latest.Audio; x == nil || x.Loudness != nil || x.Wave[0] != 100 || x.Wave[1] != 0 || x.Duration != 1 {
		t.Fatalf("推送端给的值做范围检查：%+v", x)
	}
	if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", Items: []protocol.AssetItem{{Key: "sfx:x", Kind: protocol.AssetAudio, Category: "音效", File: &glb}}}); err == nil || !strings.Contains(err.Error(), "OGG 或 WAV") {
		t.Fatalf("音效的主文件要是音效：%v", err)
	}
	if _, err := e.h.pushAssets(e.ctx, protocol.AssetPush{ByID: true, Project: "p1", Items: []protocol.AssetItem{{Key: "sfx:y", Kind: protocol.AssetPart, Category: "音效", File: &glb, Audio: a}}}); err != nil {
		t.Fatal(err)
	}
	if y, _ := e.st.FindAsset(e.ctx, "p1", "sfx:y"); y.Latest.Audio != nil {
		t.Fatal("不是音效的不存音频信息")
	}

	// 分享：访客能看音频信息、能播放（Sec-Fetch-Dest: audio 不算下载）；直接打开原文件算下载，默认关
	v := e.createShare(map[string]any{"name": "音效", "links": []string{"小王"}, "items": []map[string]any{{"kind": "asset_cat", "ref": "p1/音效", "auto": true}}})
	l := linkByName(v, "小王")
	e.do(e.owner, "PUT", "/api/shares/"+v.ID+"/links/"+l.ID, map[string]any{"perm": "upload"}, nil, 200)
	g := e.guest()
	base := "/api/share/" + l.Token
	var got store.AssetDetail
	e.do(g, "GET", base+"/assets/"+d.Code, nil, &got, 200)
	if got.Latest == nil || got.Latest.Audio == nil || got.Latest.Audio.Channels != 2 || len(got.Variants) != 1 {
		t.Fatalf("访客看到音频信息和变体：%+v", got.Latest)
	}
	e.do(g, "GET", base+"/files/"+wav.ID, nil, nil, 206, "Sec-Fetch-Dest", "audio", "Range", "bytes=0-99")
	e.do(g, "GET", base+"/files/"+wav.ID, nil, nil, 403, "Sec-Fetch-Dest", "document")

	// 访客上传 WAV，收下为音效的新版本；GLB 不能收为音效的新版本
	up := e.upload(g, l.Token, d.Code, 1, "hit_v2.wav", audiometa.SineWAV(22050, 1, 0.25, 440, 0.5, 16), 200)
	wrong := e.upload(g, l.Token, d.Code, 1, "x.glb", append(append([]byte{}, glbHead...), 'w'), 200)
	place := func(id string, want int) (store.ShareComment, []byte) {
		var out struct {
			Comment store.ShareComment `json:"comment"`
		}
		_, raw := e.do(e.owner, "POST", "/api/share-comments/"+id+"/place", map[string]any{"as": "version"}, &out, want)
		return out.Comment, raw
	}
	if _, raw := place(wrong.ID, 400); !strings.Contains(string(raw), "音效的新版本只能是 OGG 或 WAV") {
		t.Fatalf("类型不符：%s", raw)
	}
	if c, _ := place(up.ID, 200); c.ResultRef != "v2" {
		t.Fatalf("收下为新版本：%+v", c)
	}
	d2, _ := e.st.FindAsset(e.ctx, "p1", "sfx:hit")
	if x := d2.Latest.Audio; d2.Version != 2 || x == nil || x.Channels != 1 || x.SampleRate != 22050 || x.Duration != 0.25 || x.Loudness == nil {
		t.Fatalf("访客版本的音频信息：%+v", x)
	}
}
