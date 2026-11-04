package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Voices are the Piper voices go24 offers (deploy/piper downloads them); the first is the default.
var Voices = []string{"ru_RU-irina-medium", "ru_RU-dmitri-medium", "ru_RU-denis-medium", "ru_RU-ruslan-medium",
	"en_US-amy-medium", "en_US-ryan-medium"}

// MaxText is the longest text spoken at once: about a minute of speech, the longest sound of telephony.
const MaxText = 1000

var ErrText = errors.New("speech: no text or too long")

// Synth is the Piper speech synthesis service; nil — no synthesis.
type Synth struct {
	Base string
	HTTP *http.Client
}

func NewSynth(base string) *Synth {
	if base == "" {
		return nil
	}
	return &Synth{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 2 * time.Minute}}
}

// Speak gives the WAV of the text read by the voice (an unknown one — the default) at the rate (1 — normal, 0.5–2).
func (s *Synth) Speak(ctx context.Context, text, voice string, rate float64) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" || utf8.RuneCountInString(text) > MaxText {
		return nil, ErrText
	}
	if !slices.Contains(Voices, voice) {
		voice = Voices[0]
	}
	if rate < 0.5 || rate > 2 {
		rate = 1
	}
	body, _ := json.Marshal(map[string]any{"text": text, "voice": voice, "length_scale": 1 / rate})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Base+"/synthesize", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("piper: %w", err)
	}
	defer res.Body.Close()
	wav, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("piper: HTTP %d", res.StatusCode)
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		return nil, errors.New("piper: not a WAV")
	}
	return wav, nil
}
