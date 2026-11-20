package assetpush

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/NekroAI/kitestring/internal/assetfmt"
	"github.com/NekroAI/kitestring/internal/audiometa"
	"github.com/NekroAI/kitestring/internal/protocol"
)

// 音效素材（R-381）：主文件是 OGG（Vorbis / Opus）或 WAV。时长、声道、采样率、响度、峰值、波形由推送端从文件读
// （internal/audiometa；OGG 的响度、峰值、波形要本机有 ffmpeg 解码）。读不出的可在 asset.json 的 audio 里声明：
//   "audio": {"loudness_lufs": -18, "peak_dbfs": -1, "duration_s": 1.2, "channels": 2, "sample_rate": 48000}
// 优先级：文件里读出的 > asset.json 声明；声明只补读不出的字段（记进 Declared，网页上标「声明」），和读出的不一致时提示。

type audioDecl struct {
	Duration   *float64 `json:"duration_s"`
	Channels   *int     `json:"channels"`
	SampleRate *int     `json:"sample_rate"`
	Loudness   *float64 `json:"loudness_lufs"`
	Peak       *float64 `json:"peak_dbfs"`
}

// fileKind 按扩展名定素材的种类；不收的类型返回空。
func fileKind(file string) string {
	ext := strings.ToLower(filepath.Ext(file))
	switch {
	case ext == ".glb":
		return protocol.AssetPart
	case imageExt[ext], assetfmt.SVGExts[ext]:
		return protocol.AssetImage
	case audiometa.Exts[ext]:
		return protocol.AssetAudio
	case assetfmt.FontExts[ext]:
		return protocol.AssetFont
	}
	return ""
}

const unsupportedType = "（现在只收 GLB、图片（含 SVG）、音效 OGG / WAV 和字体 TTF / OTF / WOFF2）"

// audioInfo 读音效文件并合并 asset.json 的 audio 声明，返回音频信息、错误和提示。
func audioInfo(file string, raw json.RawMessage, ffmpeg bool) (*protocol.AssetAudioInfo, []string, []string) {
	var errs, warns []string
	info, err := audiometa.Read(file, audiometa.Options{FFmpeg: ffmpeg})
	if err != nil {
		return nil, []string{"音效文件 " + filepath.Base(file) + " 读不出来：" + err.Error()}, nil
	}
	var d audioDecl
	if len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &d) != nil {
			return nil, []string{`audio 要写成对象，如 {"loudness_lufs": -18, "peak_dbfs": -1}`}, nil
		}
	}
	bad := func(name string, ok bool) {
		if !ok {
			errs = append(errs, "audio."+name+" 的值不合理")
		}
	}
	if d.Duration != nil {
		bad("duration_s", *d.Duration > 0 && *d.Duration < 24*3600)
	}
	if d.Channels != nil {
		bad("channels", *d.Channels >= 1 && *d.Channels <= 64)
	}
	if d.SampleRate != nil {
		bad("sample_rate", *d.SampleRate >= 1000 && *d.SampleRate <= 768000)
	}
	if d.Loudness != nil {
		bad("loudness_lufs", *d.Loudness >= -100 && *d.Loudness <= 10)
	}
	if d.Peak != nil {
		bad("peak_dbfs", *d.Peak >= -200 && *d.Peak <= 10)
	}
	if len(errs) > 0 {
		return nil, errs, nil
	}
	differ := func(name, declared, read string) {
		warns = append(warns, fmt.Sprintf("asset.json 声明的 audio.%s（%s）和文件读出的（%s）不一致，按文件", name, declared, read))
	}
	num := func(v float64) string { return fmt.Sprintf("%g", v) }
	// 时长、声道、采样率：OGG 和 WAV 都读得出；读不出（时长为 0）时才用声明
	switch {
	case d.Duration == nil:
	case info.Duration == 0:
		info.Duration, info.Declared = *d.Duration, append(info.Declared, "duration_s")
	case math.Abs(*d.Duration-info.Duration) > math.Max(0.05, info.Duration*0.01):
		differ("duration_s", num(*d.Duration), num(info.Duration))
	}
	if d.Channels != nil && *d.Channels != info.Channels {
		differ("channels", num(float64(*d.Channels)), num(float64(info.Channels)))
	}
	if d.SampleRate != nil && *d.SampleRate != info.SampleRate {
		differ("sample_rate", num(float64(*d.SampleRate)), num(float64(info.SampleRate)))
	}
	switch {
	case d.Loudness == nil:
	case info.Loudness == nil:
		v := *d.Loudness
		info.Loudness, info.Declared = &v, append(info.Declared, "loudness_lufs")
	case math.Abs(*d.Loudness-*info.Loudness) > 1:
		differ("loudness_lufs", num(*d.Loudness), num(*info.Loudness))
	}
	switch {
	case d.Peak == nil:
	case info.Peak == nil:
		v := *d.Peak
		info.Peak, info.Declared = &v, append(info.Declared, "peak_dbfs")
	case math.Abs(*d.Peak-*info.Peak) > 1:
		differ("peak_dbfs", num(*d.Peak), num(*info.Peak))
	}
	if info.Duration == 0 {
		warns = append(warns, "读不出时长，可在 asset.json 的 audio.duration_s 写上")
	}
	if info.Loudness == nil {
		if info.Codec == "pcm" {
			warns = append(warns, "整段静音，没有响度")
		} else {
			warns = append(warns, "读不出响度（OGG 要本机装了 ffmpeg 才能测），可在 asset.json 的 audio.loudness_lufs 写上")
		}
	}
	return &info, nil, warns
}

// AudioText 把音频信息写成一句（校验报告、ks asset get 用）：「1.2 秒 · 立体声 · 48 kHz · -18.4 LUFS · 峰值 -1 dBFS」，取自声明的标「声明」。
func AudioText(a *protocol.AssetAudioInfo) string {
	if a == nil {
		return ""
	}
	declared := func(k string) string {
		for _, d := range a.Declared {
			if d == k {
				return "（声明）"
			}
		}
		return ""
	}
	var parts []string
	if a.Duration > 0 {
		parts = append(parts, fmt.Sprintf("%g 秒", a.Duration)+declared("duration_s"))
	}
	switch a.Channels {
	case 0:
	case 1:
		parts = append(parts, "单声道")
	case 2:
		parts = append(parts, "立体声")
	default:
		parts = append(parts, fmt.Sprintf("%d 声道", a.Channels))
	}
	if a.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("%g kHz", float64(a.SampleRate)/1000))
	}
	if a.Loudness != nil {
		parts = append(parts, fmt.Sprintf("%g LUFS", *a.Loudness)+declared("loudness_lufs"))
	}
	if a.Peak != nil {
		parts = append(parts, fmt.Sprintf("峰值 %g dBFS", *a.Peak)+declared("peak_dbfs"))
	}
	return strings.Join(parts, " · ")
}
