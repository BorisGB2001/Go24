package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Request is a call of a provider's API: a form (Form) or a JSON body (JSON) posted to URL.
type Request struct {
	URL        string
	Form       url.Values
	JSON       any
	User, Pass string // basic authorization (Twilio)
	Bearer     string // the gateway's token
}

// Response is the provider's answer.
type Response struct {
	Status int
	Body   []byte
}

// Endpoints of the providers' APIs (tests point them elsewhere).
var (
	SMSRuURL  = "https://sms.ru/sms/send"
	SMSCURL   = "https://smsc.ru/sys/send.php"
	TwilioURL = "https://api.twilio.com/2010-04-01/Accounts/%s/Messages.json"
)

var client = &http.Client{Timeout: 20 * time.Second}

func (s *Store) post(ctx context.Context, req Request) (Response, error) {
	if s.Post != nil {
		return s.Post(ctx, req)
	}
	var body io.Reader
	ctype := "application/x-www-form-urlencoded"
	if req.JSON != nil {
		b, err := json.Marshal(req.JSON)
		if err != nil {
			return Response{}, err
		}
		body, ctype = bytes.NewReader(b), "application/json"
	} else {
		body = strings.NewReader(req.Form.Encode())
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, req.URL, body)
	if err != nil {
		return Response{}, err
	}
	r.Header.Set("Content-Type", ctype)
	if req.User != "" {
		r.SetBasicAuth(req.User, req.Pass)
	}
	if req.Bearer != "" {
		r.Header.Set("Authorization", "Bearer "+req.Bearer)
	}
	resp, err := client.Do(r)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return Response{Status: resp.StatusCode, Body: b}, err
}

// deliver hands the message to the provider: its id there, or why it refused.
func (s *Store) deliver(ctx context.Context, x Sender, secret, phone, text string) (string, error) {
	digits := strings.TrimPrefix(phone, "+")
	switch x.Kind {
	case "smsru":
		f := url.Values{"api_id": {secret}, "to": {digits}, "msg": {text}, "json": {"1"}}
		if x.From != "" {
			f.Set("from", x.From)
		}
		resp, err := s.post(ctx, Request{URL: SMSRuURL, Form: f})
		if err != nil {
			return "", err
		}
		var out struct {
			Status     string `json:"status"`
			StatusText string `json:"status_text"`
			SMS        map[string]struct {
				Status     string `json:"status"`
				StatusText string `json:"status_text"`
				ID         string `json:"sms_id"`
			} `json:"sms"`
		}
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return "", httpErr(resp)
		}
		if out.Status != "OK" {
			if out.StatusText == "" && out.Status == "" {
				return "", httpErr(resp)
			}
			return "", errors.New(nonEmpty(out.StatusText, out.Status))
		}
		for _, m := range out.SMS {
			if m.Status != "OK" {
				return "", errors.New(nonEmpty(m.StatusText, m.Status))
			}
			return m.ID, nil
		}
		return "", nil
	case "smsc":
		f := url.Values{"login": {x.Login}, "psw": {secret}, "phones": {digits}, "mes": {text}, "fmt": {"3"}, "charset": {"utf-8"}}
		if x.From != "" {
			f.Set("sender", x.From)
		}
		resp, err := s.post(ctx, Request{URL: SMSCURL, Form: f})
		if err != nil {
			return "", err
		}
		var out struct {
			ID    json.Number `json:"id"`
			Error string      `json:"error"`
		}
		if err := json.Unmarshal(resp.Body, &out); err != nil {
			return "", httpErr(resp)
		}
		if out.Error != "" {
			return "", errors.New(out.Error)
		}
		return out.ID.String(), nil
	case "twilio":
		f := url.Values{"To": {phone}, "From": {x.From}, "Body": {text}}
		resp, err := s.post(ctx, Request{URL: fmt.Sprintf(TwilioURL, url.PathEscape(x.Login)), Form: f, User: x.Login, Pass: secret})
		if err != nil {
			return "", err
		}
		var out struct {
			SID     string `json:"sid"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(resp.Body, &out)
		if resp.Status/100 != 2 {
			if out.Message != "" {
				return "", errors.New(out.Message)
			}
			return "", httpErr(resp)
		}
		return out.SID, nil
	case "gateway":
		resp, err := s.post(ctx, Request{URL: x.Login, JSON: map[string]string{"phone": phone, "text": text, "sender": x.From}, Bearer: secret})
		if err != nil {
			return "", err
		}
		var out struct {
			ID    json.RawMessage `json:"id"`
			Error string          `json:"error"`
		}
		_ = json.Unmarshal(resp.Body, &out)
		if resp.Status/100 != 2 {
			if out.Error != "" {
				return "", errors.New(out.Error)
			}
			return "", httpErr(resp)
		}
		return strings.Trim(string(out.ID), `"`), nil
	}
	return "", errSender
}

func httpErr(r Response) error {
	b := strings.TrimSpace(string(r.Body))
	if len(b) > 200 {
		b = b[:200]
	}
	return fmt.Errorf("HTTP %d %s", r.Status, b)
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
