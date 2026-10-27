// Package speech turns voice messages into text with the go24 Whisper service (whisper-asr-webservice).
package speech

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is the Whisper service; nil — no speech recognition.
type Client struct {
	Base string
	HTTP *http.Client
}

func New(base string) *Client {
	if base == "" {
		return nil
	}
	return &Client{Base: strings.TrimRight(base, "/"), HTTP: &http.Client{Timeout: 5 * time.Minute}}
}

// Transcribe gives the text of the recording; lang ("ru", "en") helps, "" — Whisper detects the language itself.
func (c *Client) Transcribe(ctx context.Context, name string, audio io.Reader, lang string) (string, error) {
	text, err := c.asr(ctx, name, audio, lang, "txt", 1<<20)
	return strings.TrimSpace(string(text)), err
}

// Segment is a piece of speech with its time from the start of the recording.
type Segment struct {
	Start float64 `json:"start"` // seconds
	End   float64 `json:"end"`
	Text  string  `json:"text"`
}

// Segments gives the text of a long recording piece by piece with its time; the audio is streamed to Whisper.
func (c *Client) Segments(ctx context.Context, name string, audio io.Reader, lang string) ([]Segment, error) {
	body, err := c.asr(ctx, name, audio, lang, "json", 32<<20)
	if err != nil {
		return nil, err
	}
	var out struct {
		Segments []Segment `json:"segments"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("whisper: %w", err)
	}
	segs := out.Segments[:0]
	for _, s := range out.Segments {
		if s.Text = strings.TrimSpace(s.Text); s.Text != "" {
			segs = append(segs, s)
		}
	}
	return segs, nil
}

// asr sends the recording to /asr (the form is written while it is sent) and reads up to max bytes of the answer.
func (c *Client) asr(ctx context.Context, name string, audio io.Reader, lang, output string, max int64) ([]byte, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		fw, err := mw.CreateFormFile("audio_file", name)
		if err == nil {
			_, err = io.Copy(fw, audio)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	defer pr.Close()
	// vad_filter: silence and noise are cut off first — without it Whisper "hears" words in a recording with no speech
	q := url.Values{"task": {"transcribe"}, "output": {output}, "encode": {"true"}, "vad_filter": {"true"}}
	if lang != "" {
		q.Set("language", lang)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/asr?"+q.Encode(), pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whisper: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, max))
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("whisper: HTTP %d", res.StatusCode)
	}
	return body, nil
}
