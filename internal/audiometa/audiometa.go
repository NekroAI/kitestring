// Package audiometa 读音效文件的时长、声道、采样率、响度、峰值和波形（R-381），不引入第三方依赖。
//
//   - WAV（PCM 8 / 16 / 24 / 32 位整数，32 / 64 位浮点，含 WAVE_FORMAT_EXTENSIBLE）：全部从文件读出。
//   - OGG（Vorbis / Opus）：时长、声道、采样率读文件头和最后一页的 granule；
//     响度、峰值、波形要解码，Options.FFmpeg 为 true 且本机有 ffmpeg 时用它解成 PCM 再算，否则留空。
//
// 响度是 ITU-R BS.1770-4 的综合响度（LUFS，K 加权、400 ms 块、绝对门限 -70 LUFS、相对门限 -10 LU）；
// 不足 400 ms 的短音效按整段算一个块。峰值是采样峰值（dBFS）。
package audiometa

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/NekroAI/kitestring/internal/protocol"
)

// WaveBins 是波形的段数。
const WaveBins = 64

// Exts 是资源库收的音效扩展名。
var Exts = map[string]bool{".ogg": true, ".oga": true, ".opus": true, ".wav": true}

// IsAudioExt：按扩展名判断是不是音效文件。
func IsAudioExt(path string) bool { return Exts[strings.ToLower(filepath.Ext(path))] }

// Mime 认文件头：OggS → audio/ogg，RIFF…WAVE → audio/wav；别的返回空。
func Mime(head []byte) string {
	switch {
	case len(head) >= 4 && bytes.Equal(head[:4], []byte("OggS")):
		return "audio/ogg"
	case len(head) >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("WAVE")):
		return "audio/wav"
	}
	return ""
}

// IsAudioMime：资源库收的音效类型。
func IsAudioMime(m string) bool { return m == "audio/ogg" || m == "audio/wav" }

// Options 控制读取方式。
type Options struct {
	FFmpeg bool // OGG 用本机 ffmpeg 解码，算响度、峰值和波形（找不到 ffmpeg 时跳过）
}

// Read 读一个音效文件。格式不对（不是 WAV / OGG Vorbis / OGG Opus，或文件头坏了）返回 error。
func Read(path string, o Options) (protocol.AssetAudioInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return protocol.AssetAudioInfo{}, err
	}
	defer f.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	switch Mime(head[:n]) {
	case "audio/wav":
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return protocol.AssetAudioInfo{}, err
		}
		return readWAV(f)
	case "audio/ogg":
		info, err := readOgg(f)
		if err != nil {
			return info, err
		}
		if o.FFmpeg {
			if pcm, ok := decodeFFmpeg(path, info.Channels, decodeRate(info)); ok {
				analyze(&info, pcm, info.Channels, decodeRate(info))
			}
		}
		return info, nil
	}
	return protocol.AssetAudioInfo{}, errors.New("不是 WAV 或 OGG 文件（文件头不对）")
}

// decodeRate：Opus 一律解成 48 kHz（Opus 内部的采样率）。
func decodeRate(info protocol.AssetAudioInfo) int {
	if info.Codec == "opus" {
		return 48000
	}
	return info.SampleRate
}

// ---------- WAV ----------

func readWAV(r io.ReadSeeker) (protocol.AssetAudioInfo, error) {
	info := protocol.AssetAudioInfo{Codec: "pcm"}
	var riff [12]byte
	if _, err := io.ReadFull(r, riff[:]); err != nil {
		return info, errors.New("WAV 文件头不完整")
	}
	var format, channels, block, bits uint16
	var rate uint32
	haveFmt := false
	for {
		var ch [8]byte
		if _, err := io.ReadFull(r, ch[:]); err != nil {
			return info, errors.New("WAV 里没有 data 块")
		}
		id, size := string(ch[:4]), binary.LittleEndian.Uint32(ch[4:])
		switch id {
		case "fmt ":
			if size < 16 || size > 1<<16 {
				return info, errors.New("WAV 的 fmt 块长度不对")
			}
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return info, errors.New("WAV 的 fmt 块不完整")
			}
			format, channels = binary.LittleEndian.Uint16(b[0:]), binary.LittleEndian.Uint16(b[2:])
			rate, block, bits = binary.LittleEndian.Uint32(b[4:]), binary.LittleEndian.Uint16(b[12:]), binary.LittleEndian.Uint16(b[14:])
			if format == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE：子格式 GUID 的前两字节是真正的格式
				format = binary.LittleEndian.Uint16(b[24:])
			}
			if size%2 == 1 {
				_, _ = r.Seek(1, io.SeekCurrent)
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return info, errors.New("WAV 的 data 块在 fmt 块前面")
			}
			if format != 1 && format != 3 {
				return info, fmt.Errorf("WAV 的编码不支持（格式 %d），只收 PCM 和浮点", format)
			}
			if channels == 0 || rate == 0 || block == 0 || bits == 0 || int(block) != int(channels)*int(bits/8) {
				return info, errors.New("WAV 的声道、采样率或位深不对")
			}
			if format == 3 && bits != 32 && bits != 64 || format == 1 && bits != 8 && bits != 16 && bits != 24 && bits != 32 {
				return info, fmt.Errorf("WAV 的位深不支持：%d 位", bits)
			}
			info.Channels, info.SampleRate = int(channels), int(rate)
			// data 块长度可能写成 0 或超出文件（边录边写的文件），以实际读到的为准
			data, err := io.ReadAll(io.LimitReader(r, int64(size)))
			if err != nil {
				return info, err
			}
			if size == 0 || size == 0xFFFFFFFF {
				rest, _ := io.ReadAll(r)
				data = append(data, rest...)
			}
			frames := len(data) / int(block)
			info.Duration = round(float64(frames)/float64(rate), 3)
			pcm := make([]float64, frames*int(channels))
			bps := int(bits / 8)
			for i := range pcm {
				pcm[i] = sample(data[i*bps:], format, bits)
			}
			analyze(&info, pcm, int(channels), int(rate))
			return info, nil
		default:
			if _, err := r.Seek(int64(size)+int64(size%2), io.SeekCurrent); err != nil {
				return info, errors.New("WAV 块长度不对")
			}
		}
	}
}

func sample(b []byte, format, bits uint16) float64 {
	switch {
	case format == 3 && bits == 32:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(b)))
	case format == 3:
		return math.Float64frombits(binary.LittleEndian.Uint64(b))
	case bits == 8:
		return (float64(b[0]) - 128) / 128
	case bits == 16:
		return float64(int16(binary.LittleEndian.Uint16(b))) / 32768
	case bits == 24:
		v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
		return float64(v) / 8388608
	default:
		return float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648
	}
}

// ---------- OGG ----------

// readOgg 读第一页的编码头（Vorbis 或 Opus）得到声道和采样率，再读最后一页的 granule 算时长。
func readOgg(f *os.File) (protocol.AssetAudioInfo, error) {
	info := protocol.AssetAudioInfo{}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return info, err
	}
	var hdr [27]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[:4]) != "OggS" {
		return info, errors.New("OGG 文件头不完整")
	}
	serial := binary.LittleEndian.Uint32(hdr[14:])
	segs := make([]byte, hdr[26])
	if _, err := io.ReadFull(f, segs); err != nil {
		return info, errors.New("OGG 文件头不完整")
	}
	plen := 0
	for _, s := range segs {
		plen += int(s)
		if s < 255 {
			break
		}
	}
	pkt := make([]byte, plen)
	if _, err := io.ReadFull(f, pkt); err != nil {
		return info, errors.New("OGG 第一页不完整")
	}
	preSkip := int64(0)
	switch {
	case len(pkt) >= 16 && pkt[0] == 1 && string(pkt[1:7]) == "vorbis":
		info.Codec = "vorbis"
		info.Channels = int(pkt[11])
		info.SampleRate = int(binary.LittleEndian.Uint32(pkt[12:]))
	case len(pkt) >= 19 && string(pkt[:8]) == "OpusHead":
		info.Codec = "opus"
		info.Channels = int(pkt[9])
		preSkip = int64(binary.LittleEndian.Uint16(pkt[10:]))
		info.SampleRate = int(binary.LittleEndian.Uint32(pkt[12:])) // 原始采样率（只作信息）；时长按 48 kHz 算
		if info.SampleRate == 0 {
			info.SampleRate = 48000
		}
	default:
		return info, errors.New("OGG 里不是 Vorbis 或 Opus 音频")
	}
	if info.Channels == 0 || info.SampleRate == 0 {
		return info, errors.New("OGG 的声道或采样率不对")
	}
	granule, err := lastGranule(f, serial)
	if err != nil {
		return info, err
	}
	rate := float64(info.SampleRate)
	if info.Codec == "opus" {
		rate = 48000
		granule -= preSkip
	}
	if granule > 0 {
		info.Duration = round(float64(granule)/rate, 3)
	}
	return info, nil
}

// lastGranule 从文件末尾往前找同一条流的最后一页，取它的 granule。
func lastGranule(f *os.File, serial uint32) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()
	for back := int64(64 << 10); ; back *= 4 {
		start := max(0, size-back)
		buf := make([]byte, size-start)
		if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		for i := len(buf) - 27; i >= 0; i-- {
			if buf[i] != 'O' || string(buf[i:i+4]) != "OggS" || binary.LittleEndian.Uint32(buf[i+14:]) != serial {
				continue
			}
			g := int64(binary.LittleEndian.Uint64(buf[i+6:]))
			if g >= 0 {
				return g, nil
			}
		}
		if start == 0 {
			return 0, errors.New("OGG 里找不到结束页，文件可能不完整")
		}
	}
}

// decodeFFmpeg 用本机的 ffmpeg 把文件解成 32 位浮点 PCM（交错），找不到 ffmpeg 或解码失败返回 false。
func decodeFFmpeg(path string, channels, rate int) ([]float64, bool) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil || channels <= 0 || rate <= 0 {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-v", "error", "-nostdin", "-i", path, "-ac", fmt.Sprint(channels), "-ar", fmt.Sprint(rate), "-f", "f32le", "-acodec", "pcm_f32le", "-")
	out, err := cmd.Output()
	if err != nil || len(out) < 4 {
		return nil, false
	}
	pcm := make([]float64, len(out)/4)
	for i := range pcm {
		pcm[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(out[i*4:])))
	}
	return pcm, true
}

// ---------- 分析：响度、峰值、波形 ----------

// analyze 从交错的 PCM（-1..1）算综合响度、采样峰值和波形，写进 info。
func analyze(info *protocol.AssetAudioInfo, pcm []float64, channels, rate int) {
	if channels <= 0 || rate <= 0 || len(pcm) < channels {
		return
	}
	frames := len(pcm) / channels
	peak := 0.0
	wave := make([]float64, WaveBins)
	for i := 0; i < frames; i++ {
		bin := i * WaveBins / frames
		for c := 0; c < channels; c++ {
			v := math.Abs(pcm[i*channels+c])
			peak = max(peak, v)
			wave[bin] = max(wave[bin], v)
		}
	}
	info.Wave = make([]int, WaveBins)
	for i, v := range wave {
		info.Wave[i] = int(math.Round(math.Min(v, 1) * 100))
	}
	if peak > 0 {
		p := round(20*math.Log10(peak), 1)
		info.Peak = &p
	}
	if l, ok := loudness(pcm, channels, rate); ok {
		l = round(l, 1)
		info.Loudness = &l
	}
}

type biquad struct{ b0, b1, b2, a1, a2, z1, z2 float64 }

func (q *biquad) run(x float64) float64 {
	y := q.b0*x + q.z1
	q.z1 = q.b1*x - q.a1*y + q.z2
	q.z2 = q.b2*x - q.a2*y
	return y
}

// kWeight 是 BS.1770 的 K 加权（高架 + 高通），系数按采样率算（与 libebur128 相同）。
func kWeight(rate int) (biquad, biquad) {
	fs := float64(rate)
	f0, g, q := 1681.974450955533, 3.999843853973347, 0.7071752369554196
	k := math.Tan(math.Pi * f0 / fs)
	vh := math.Pow(10, g/20)
	vb := math.Pow(vh, 0.4996667741545416)
	a0 := 1 + k/q + k*k
	shelf := biquad{b0: (vh + vb*k/q + k*k) / a0, b1: 2 * (k*k - vh) / a0, b2: (vh - vb*k/q + k*k) / a0, a1: 2 * (k*k - 1) / a0, a2: (1 - k/q + k*k) / a0}
	f0, q = 38.13547087602444, 0.5003270373238773
	k = math.Tan(math.Pi * f0 / fs)
	a0 = 1 + k/q + k*k
	hp := biquad{b0: 1, b1: -2, b2: 1, a1: 2 * (k*k - 1) / a0, a2: (1 - k/q + k*k) / a0}
	return shelf, hp
}

// channelGain：BS.1770 的声道权重——5.1 的 LFE（第 4 声道）不计，环绕声道（第 5、6 声道）1.41，其余 1。
func channelGain(channels, c int) float64 {
	if channels == 6 {
		switch c {
		case 3:
			return 0
		case 4, 5:
			return 1.41
		}
	}
	return 1
}

// loudness 是综合响度（LUFS）；全静音或太短时返回 false。
func loudness(pcm []float64, channels, rate int) (float64, bool) {
	frames := len(pcm) / channels
	if frames == 0 {
		return 0, false
	}
	// 每声道 K 加权后的平方，按 100 ms 一段累加（400 ms 块 = 连续 4 段，75% 重叠）
	step := rate / 10
	if step == 0 {
		return 0, false
	}
	nseg := (frames + step - 1) / step
	seg := make([]float64, nseg)
	for c := 0; c < channels; c++ {
		gain := channelGain(channels, c)
		if gain == 0 {
			continue
		}
		s1, s2 := kWeight(rate)
		for i := 0; i < frames; i++ {
			y := s2.run(s1.run(pcm[i*channels+c]))
			seg[i/step] += gain * y * y
		}
	}
	block := 4 * step
	var zs []float64
	if frames < block {
		// 不足 400 ms：整段算一个块
		sum := 0.0
		for _, v := range seg {
			sum += v
		}
		zs = append(zs, sum/float64(frames))
	} else {
		full := frames / step // 完整的 100 ms 段数
		for i := 0; i+4 <= full; i++ {
			zs = append(zs, (seg[i]+seg[i+1]+seg[i+2]+seg[i+3])/float64(block))
		}
	}
	lufs := func(z float64) float64 { return -0.691 + 10*math.Log10(z) }
	gated := func(th float64) (float64, int) {
		sum, n := 0.0, 0
		for _, z := range zs {
			if z > 0 && lufs(z) > th {
				sum += z
				n++
			}
		}
		return sum, n
	}
	sum, n := gated(-70)
	if n == 0 {
		return 0, false
	}
	rel := lufs(sum/float64(n)) - 10
	sum, n = gated(rel)
	if n == 0 {
		return 0, false
	}
	return lufs(sum / float64(n)), true
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}
