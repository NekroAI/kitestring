package audiometa

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func write(t *testing.T, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// R-381：WAV 的时长、声道、采样率、峰值、综合响度、波形。立体声 1 kHz 正弦、振幅 0.1（-20 dBFS）：
// BS.1770 下 1 kHz 的 K 加权约 0 dB，两个声道各 -23.01 LUFS 相加 → 约 -20 LUFS。
func TestReadWAV(t *testing.T) {
	for _, bits := range []int{8, 16, 24, 32} {
		p := write(t, "s.wav", SineWAV(48000, 2, 1.5, 1000, 0.1, bits))
		info, err := Read(p, Options{})
		if err != nil {
			t.Fatal(bits, err)
		}
		if info.Codec != "pcm" || info.Channels != 2 || info.SampleRate != 48000 || info.Duration != 1.5 {
			t.Fatalf("%d 位：%+v", bits, info)
		}
		tol := 0.1
		if bits == 8 {
			tol = 0.5
		}
		if info.Peak == nil || math.Abs(*info.Peak+20) > tol || info.Loudness == nil || math.Abs(*info.Loudness+20) > 0.3+tol {
			t.Fatalf("%d 位的峰值 / 响度：%v %v", bits, deref(info.Peak), deref(info.Loudness))
		}
		if len(info.Wave) != WaveBins || info.Wave[10] != 10 {
			t.Fatalf("%d 位的波形：%v", bits, info.Wave)
		}
	}
	// 单声道、不足 400 ms 的短音效：整段算一个块；44.1 kHz 的系数按采样率算
	info, err := Read(write(t, "short.wav", SineWAV(44100, 1, 0.2, 1000, 0.5, 16)), Options{})
	if err != nil || info.Channels != 1 || info.Duration != 0.2 || info.Loudness == nil || math.Abs(*info.Loudness-(-3.01-6.02)) > 0.5 {
		t.Fatalf("短音效：%+v %v %v", info, deref(info.Loudness), err)
	}
	// 静音：没有峰值和响度
	info, err = Read(write(t, "silent.wav", SineWAV(22050, 1, 1, 1000, 0, 16)), Options{})
	if err != nil || info.Peak != nil || info.Loudness != nil || info.Duration != 1 {
		t.Fatalf("静音：%+v %v", info, err)
	}
}

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// OGG：读编码头和结束页的 granule。Vorbis 按采样率算时长；Opus 按 48 kHz、减去 pre-skip。
func TestReadOgg(t *testing.T) {
	info, err := Read(write(t, "a.ogg", FakeOgg("vorbis", 2, 44100, 0, 88200)), Options{})
	if err != nil || info.Codec != "vorbis" || info.Channels != 2 || info.SampleRate != 44100 || info.Duration != 2 || info.Loudness != nil {
		t.Fatalf("vorbis：%+v %v", info, err)
	}
	info, err = Read(write(t, "a.opus", FakeOgg("opus", 1, 48000, 312, 48000+312)), Options{})
	if err != nil || info.Codec != "opus" || info.Channels != 1 || info.Duration != 1 {
		t.Fatalf("opus：%+v %v", info, err)
	}
	for name, b := range map[string][]byte{"bad.ogg": []byte("OggS-not-really"), "bad.wav": []byte("RIFF\x00\x00\x00\x00WAVEjunk"), "x.wav": []byte("hello")} {
		if _, err := Read(write(t, name, b), Options{}); err == nil {
			t.Fatalf("%s 应读不出来", name)
		}
	}
}

// 本机有 ffmpeg 时：真实的 OGG Vorbis 解码后算响度，和原 WAV 相差不超过 1 LU。
func TestReadOggFFmpeg(t *testing.T) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("没有 ffmpeg")
	}
	wav := write(t, "s.wav", SineWAV(48000, 2, 1, 440, 0.25, 16))
	ogg := filepath.Join(filepath.Dir(wav), "s.ogg")
	if out, err := exec.Command(bin, "-v", "error", "-i", wav, "-c:a", "libvorbis", "-q:a", "6", ogg).CombinedOutput(); err != nil {
		if out2, err2 := exec.Command(bin, "-v", "error", "-y", "-i", wav, "-c:a", "vorbis", "-strict", "-2", ogg).CombinedOutput(); err2 != nil {
			t.Skipf("ffmpeg 编不了 vorbis：%s %s", out, out2)
		}
	}
	w, _ := Read(wav, Options{})
	o, err := Read(ogg, Options{FFmpeg: true})
	if err != nil || o.Codec != "vorbis" || o.Channels != 2 || math.Abs(o.Duration-1) > 0.05 || o.Loudness == nil || math.Abs(*o.Loudness-*w.Loudness) > 1 || len(o.Wave) != WaveBins {
		t.Fatalf("ogg：%+v %v（wav %v）%v", o, deref(o.Loudness), deref(w.Loudness), err)
	}
	if n, _ := Read(ogg, Options{}); n.Loudness != nil {
		t.Fatal("不用 ffmpeg 时 OGG 不应有响度")
	}
}
