package audiometa

import (
	"bytes"
	"encoding/binary"
	"math"
)

// 测试用的合成音频（各包的单测共用）：正弦波 WAV，和只有文件头、结束页的 OGG（不含音频数据，只够读声道、采样率和时长）。

// SineWAV 生成 PCM WAV：freq Hz 正弦波，各声道同样的振幅 amp（0–1），bits 取 8 / 16 / 24 / 32（32 位写成浮点）。
func SineWAV(rate, channels int, seconds, freq, amp float64, bits int) []byte {
	frames := int(float64(rate) * seconds)
	bps := bits / 8
	format := uint16(1)
	if bits == 32 {
		format = 3
	}
	data := make([]byte, 0, frames*channels*bps)
	for i := 0; i < frames; i++ {
		v := amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))
		for c := 0; c < channels; c++ {
			switch bits {
			case 8:
				data = append(data, byte(int(math.Round(v*127))+128))
			case 16:
				data = binary.LittleEndian.AppendUint16(data, uint16(int16(math.Round(v*32767))))
			case 24:
				x := int32(math.Round(v * 8388607))
				data = append(data, byte(x), byte(x>>8), byte(x>>16))
			default:
				data = binary.LittleEndian.AppendUint32(data, math.Float32bits(float32(v)))
			}
		}
	}
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+len(data)))
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), format, uint16(channels), uint32(rate), uint32(rate * channels * bps), uint16(channels * bps), uint16(bits)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	b.Write(data)
	return b.Bytes()
}

// FakeOgg 生成只有编码头页和结束页的 OGG：codec 是 vorbis 或 opus，granule 是结束页的 granule（Opus 按 48 kHz、含 preSkip）。
func FakeOgg(codec string, channels, rate int, preSkip uint16, granule int64) []byte {
	var head []byte
	if codec == "opus" {
		head = append([]byte("OpusHead"), 1, byte(channels))
		head = binary.LittleEndian.AppendUint16(head, preSkip)
		head = binary.LittleEndian.AppendUint32(head, uint32(rate))
		head = append(head, 0, 0, 0)
	} else {
		head = append([]byte{1}, "vorbis"...)
		head = binary.LittleEndian.AppendUint32(head, 0)
		head = append(head, byte(channels))
		head = binary.LittleEndian.AppendUint32(head, uint32(rate))
		head = append(head, make([]byte, 12)...)
		head = append(head, 0xb8, 1)
	}
	page := func(typ byte, g int64, seq uint32, payload []byte) []byte {
		p := []byte("OggS")
		p = append(p, 0, typ)
		p = binary.LittleEndian.AppendUint64(p, uint64(g))
		p = binary.LittleEndian.AppendUint32(p, 0x1234)
		p = binary.LittleEndian.AppendUint32(p, seq)
		p = binary.LittleEndian.AppendUint32(p, 0) // CRC 不校验
		p = append(p, 1, byte(len(payload)))
		return append(p, payload...)
	}
	out := page(2, 0, 0, head)
	return append(out, page(4, granule, 1, []byte{0})...)
}
