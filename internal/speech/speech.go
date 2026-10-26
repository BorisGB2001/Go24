// Package speech turns voice messages into text with the go24 Whisper service (whisper-asr-webservice).
package speech

import (
	"bytes"
	"context"
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
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("audio_file", name)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(fw, audio); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}
	// vad_filter: silence and noise are cut off first — without it Whisper "hears" words in a recording with no speech
	q := url.Values{"task": {"transcribe"}, "output": {"txt"}, "encode": {"true"}, "vad_filter": {"true"}}
	if lang != "" {
		q.Set("language", lang)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+"/asr?"+q.Encode(), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper: %w", err)
	}
	defer res.Body.Close()
	text, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return "", fmt.Errorf("whisper: HTTP %d", res.StatusCode)
	}
	return strings.TrimSpace(string(text)), nil
}
