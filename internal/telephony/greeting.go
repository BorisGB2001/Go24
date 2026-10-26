package telephony

import (
	"bytes"
	"encoding/binary"
	"math"

	"go24/internal/i18n"
)

// Sounds of the voicemail. Asterisk's image has no sounds of its own: go24 writes the beep and the lines' greetings
// into the folder of its configuration, as WAV files Asterisk plays as they are (16-bit, 8 kHz, mono).

const (
	soundRate     = 8000
	greetingLimit = 60 // seconds
)

// Greeting checks an uploaded greeting (a PCM WAV: 8 or 16 bits, mono or stereo, any rate, up to a minute) and makes
// it a WAV Asterisk plays.
func Greeting(data []byte) ([]byte, error) {
	bad := i18n.E("err.trunk_greeting")
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return nil, bad
	}
	var format, channels, bits uint16
	var rate uint32
	var pcm []byte
	for p := 12; p+8 <= len(data); {
		id, size := string(data[p:p+4]), int(binary.LittleEndian.Uint32(data[p+4:p+8]))
		p += 8
		if size < 0 || size > len(data)-p {
			size = len(data) - p // a recorder that did not fill in the size
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, bad
			}
			format, channels = binary.LittleEndian.Uint16(data[p:]), binary.LittleEndian.Uint16(data[p+2:])
			rate, bits = binary.LittleEndian.Uint32(data[p+4:]), binary.LittleEndian.Uint16(data[p+14:])
			if format == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE: the sub-format's first two bytes
				format = binary.LittleEndian.Uint16(data[p+24:])
			}
		case "data":
			pcm = data[p : p+size]
		}
		p += size + size%2
	}
	if format != 1 || (bits != 8 && bits != 16) || channels < 1 || channels > 2 || rate < 4000 || rate > 192000 || len(pcm) == 0 {
		return nil, bad
	}
	frame := int(channels) * int(bits) / 8
	n := len(pcm) / frame
	if n > greetingLimit*int(rate) {
		return nil, i18n.E("err.trunk_greeting_long")
	}
	in := make([]float64, n) // mono, -1…1
	for i := range in {
		var sum float64
		for c := 0; c < int(channels); c++ {
			o := i*frame + c*int(bits)/8
			if bits == 8 {
				sum += (float64(pcm[o]) - 128) / 128
			} else {
				sum += float64(int16(binary.LittleEndian.Uint16(pcm[o:]))) / 32768
			}
		}
		in[i] = sum / float64(channels)
	}
	out := make([]int16, int(int64(n)*soundRate/int64(rate)))
	step := float64(rate) / soundRate
	for i := range out {
		x := float64(i) * step
		j := int(x)
		v := in[j]
		if j+1 < n {
			v += (in[j+1] - v) * (x - float64(j))
		}
		out[i] = int16(math.Max(-32768, math.Min(32767, v*32767)))
	}
	return wav(out), nil
}

// Beep is the tone before the client's message: half a second of 1 kHz.
func Beep() []byte {
	s := make([]int16, soundRate/2)
	for i := range s {
		s[i] = int16(12000 * math.Sin(2*math.Pi*1000*float64(i)/soundRate))
	}
	return wav(s)
}

// wav is a WAV file of 16-bit mono samples at 8 kHz.
func wav(s []int16) []byte {
	var b bytes.Buffer
	size := uint32(len(s) * 2)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, 36+size)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, struct {
		Size                      uint32
		Format, Channels          uint16
		Rate, ByteRate            uint32
		BlockAlign, BitsPerSample uint16
	}{16, 1, 1, soundRate, soundRate * 2, 2, 16})
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, size)
	binary.Write(&b, binary.LittleEndian, s)
	return b.Bytes()
}
