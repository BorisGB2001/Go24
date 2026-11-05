package telephony

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"go24/internal/i18n"
)

// makeWAV creates a WAV with custom format, rate, channels, bit depth, and raw PCM data.
func makeWAV(format uint16, rate uint32, channels, bits uint16, pcm []byte, extensible bool) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")

	fmtSize := uint32(16)
	if extensible {
		fmtSize = 40
	}
	totalSize := 36 + fmtSize - 16 + uint32(len(pcm))
	binary.Write(&b, binary.LittleEndian, totalSize)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, fmtSize)

	if extensible {
		binary.Write(&b, binary.LittleEndian, uint16(0xFFFE))
	} else {
		binary.Write(&b, binary.LittleEndian, format)
	}
	binary.Write(&b, binary.LittleEndian, channels)
	binary.Write(&b, binary.LittleEndian, rate)
	binary.Write(&b, binary.LittleEndian, rate*uint32(channels)*uint32(bits)/8)
	binary.Write(&b, binary.LittleEndian, channels*bits/8)
	binary.Write(&b, binary.LittleEndian, bits)

	if extensible {
		binary.Write(&b, binary.LittleEndian, uint16(22)) // cbSize
		binary.Write(&b, binary.LittleEndian, bits)       // validBitsPerSample
		binary.Write(&b, binary.LittleEndian, uint32(3))  // channelMask
		// SubFormat GUID: first 2 bytes is the actual audio format (1 = PCM)
		binary.Write(&b, binary.LittleEndian, format)
		b.Write([]byte("\x00\x00\x00\x00\x10\x00\x80\x00\x00\xaa\x00\x38\x9b\x71"))
	}

	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(len(pcm)))
	b.Write(pcm)

	return b.Bytes()
}

func TestGreetingExtensive(t *testing.T) {
	// 1. 8-bit mono at 11025 Hz
	pcm8Mono := make([]byte, 11025) // 1 second of silence (center is 128)
	for i := range pcm8Mono {
		pcm8Mono[i] = 128
	}
	wav8Mono := makeWAV(1, 11025, 1, 8, pcm8Mono, false)
	g, err := Greeting(wav8Mono)
	if err != nil {
		t.Fatalf("Greeting(8-bit mono) failed: %v", err)
	}
	// Expected output: 8000 samples of 16-bit mono = 16000 bytes PCM + 44 header
	if len(g) != 44+8000*2 {
		t.Errorf("len(g) = %d, want %d", len(g), 44+8000*2)
	}

	// 2. 8-bit stereo at 22050 Hz
	pcm8Stereo := make([]byte, 22050*2) // 1 second stereo
	for i := range pcm8Stereo {
		pcm8Stereo[i] = 128
	}
	wav8Stereo := makeWAV(1, 22050, 2, 8, pcm8Stereo, false)
	g, err = Greeting(wav8Stereo)
	if err != nil {
		t.Fatalf("Greeting(8-bit stereo) failed: %v", err)
	}
	if len(g) != 44+8000*2 {
		t.Errorf("len(g) = %d, want %d", len(g), 44+8000*2)
	}

	// 3. 16-bit stereo WAVE_FORMAT_EXTENSIBLE at 48000 Hz
	pcm16Ext := make([]byte, 48000*2*2) // 1 second stereo 16-bit
	wav16Ext := makeWAV(1, 48000, 2, 16, pcm16Ext, true)
	g, err = Greeting(wav16Ext)
	if err != nil {
		t.Fatalf("Greeting(extensible) failed: %v", err)
	}
	if len(g) != 44+8000*2 {
		t.Errorf("len(g) = %d, want %d", len(g), 44+8000*2)
	}
	// Verify output header properties: Format=1, Channels=1, Rate=8000, Bits=16
	if binary.LittleEndian.Uint16(g[20:]) != 1 || binary.LittleEndian.Uint16(g[22:]) != 1 ||
		binary.LittleEndian.Uint32(g[24:]) != 8000 || binary.LittleEndian.Uint16(g[34:]) != 16 {
		t.Errorf("output WAV format invalid: %+v", g[:44])
	}
}

func TestGreetingErrorPaths(t *testing.T) {
	var ie *i18n.Error

	// Header checks
	for _, bad := range [][]byte{
		nil,
		{},
		[]byte("RIFFshort"),
		[]byte("NOTRIFFheader123"),
		[]byte("RIFF1234NOTW"),
	} {
		if _, err := Greeting(bad); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
			t.Errorf("Greeting(%q) expected err.trunk_greeting, got %v", bad, err)
		}
	}

	// Invalid format: format != 1 (e.g. format 3 = IEEE float)
	wavFloat := makeWAV(3, 8000, 1, 16, make([]byte, 1600), false)
	if _, err := Greeting(wavFloat); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
		t.Errorf("Greeting(float format) expected err.trunk_greeting, got %v", err)
	}

	// Invalid bits: 24-bit PCM
	wav24 := makeWAV(1, 8000, 1, 24, make([]byte, 2400), false)
	if _, err := Greeting(wav24); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
		t.Errorf("Greeting(24-bit) expected err.trunk_greeting, got %v", err)
	}

	// Invalid channels: 3 channels
	wav3Ch := makeWAV(1, 8000, 3, 16, make([]byte, 4800), false)
	if _, err := Greeting(wav3Ch); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
		t.Errorf("Greeting(3 channels) expected err.trunk_greeting, got %v", err)
	}

	// Invalid rate: < 4000
	wavLowRate := makeWAV(1, 2000, 1, 16, make([]byte, 4000), false)
	if _, err := Greeting(wavLowRate); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
		t.Errorf("Greeting(low rate) expected err.trunk_greeting, got %v", err)
	}

	// Invalid rate: > 192000
	wavHighRate := makeWAV(1, 250000, 1, 16, make([]byte, 4000), false)
	if _, err := Greeting(wavHighRate); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
		t.Errorf("Greeting(high rate) expected err.trunk_greeting, got %v", err)
	}

	// Empty PCM data
	wavNoData := makeWAV(1, 8000, 1, 16, nil, false)
	if _, err := Greeting(wavNoData); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting" {
		t.Errorf("Greeting(no data) expected err.trunk_greeting, got %v", err)
	}

	// Too long greeting (> 60 seconds)
	wavTooLong := makeWAV(1, 8000, 1, 16, make([]byte, 61*8000*2), false)
	if _, err := Greeting(wavTooLong); err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_greeting_long" {
		t.Errorf("Greeting(too long) expected err.trunk_greeting_long, got %v", err)
	}
}

func TestBeepProperties(t *testing.T) {
	b := Beep()
	if len(b) != 44+4000*2 { // 0.5s at 8000 Hz, 16-bit mono = 4000 samples = 8000 bytes PCM + 44 header
		t.Fatalf("Beep() length = %d, want %d", len(b), 44+4000*2)
	}
	if string(b[:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Errorf("Beep() missing RIFF/WAVE header")
	}
	if binary.LittleEndian.Uint16(b[20:]) != 1 || binary.LittleEndian.Uint16(b[22:]) != 1 ||
		binary.LittleEndian.Uint32(b[24:]) != 8000 || binary.LittleEndian.Uint16(b[34:]) != 16 {
		t.Errorf("Beep() audio format not 8000Hz 16-bit mono")
	}
}
