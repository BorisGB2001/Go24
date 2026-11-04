package paysys

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Request is a call of a service's API: JSON (nil — none) sent with Method to URL.
type Request struct {
	Method     string
	URL        string
	JSON       any
	User, Pass string            // basic authorization (ЮKassa)
	Header     map[string]string // ЮKassa's Idempotence-Key
}

// Response is the service's answer.
type Response struct {
	Status int
	Body   []byte
}

// Endpoints of the services' APIs (tests point them elsewhere).
var (
	YooKassaURL = "https://api.yookassa.ru/v3/payments"
	TBankURL    = "https://securepay.tinkoff.ru/v2/"
)

var client = &http.Client{Timeout: 20 * time.Second}

func (s *Store) call(ctx context.Context, req Request) (Response, error) {
	if s.Call != nil {
		return s.Call(ctx, req)
	}
	var body io.Reader
	if req.JSON != nil {
		b, err := json.Marshal(req.JSON)
		if err != nil {
			return Response{}, err
		}
		body = bytes.NewReader(b)
	}
	r, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return Response{}, err
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if req.User != "" {
		r.SetBasicAuth(req.User, req.Pass)
	}
	for k, v := range req.Header {
		r.Header.Set(k, v)
	}
	resp, err := client.Do(r)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return Response{Status: resp.StatusCode, Body: b}, err
}

func httpErr(resp Response) error {
	return fmt.Errorf("HTTP %d: %s", resp.Status, cut(strings.TrimSpace(string(resp.Body)), 300))
}

// yooAmount is ЮKassa's sum: a string with two decimals.
type yooAmount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

// yooPayment is ЮKassa's payment object (what go24 reads of it).
type yooPayment struct {
	ID           string    `json:"id"`
	Status       string    `json:"status"`
	Paid         bool      `json:"paid"`
	Amount       yooAmount `json:"amount"`
	Confirmation struct {
		URL string `json:"confirmation_url"`
	} `json:"confirmation"`
	Description string `json:"description"` // of an error
	Code        string `json:"code"`
}

func (p yooPayment) err(resp Response) error {
	if p.Description != "" {
		return errors.New(p.Description)
	}
	return httpErr(resp)
}

// tbankToken signs a request of Т-Банк: the values of its plain fields and the password, ordered by the names, in
// SHA-256.
func tbankToken(fields map[string]any, password string) string {
	vals := map[string]string{"Password": password}
	for k, v := range fields {
		switch v := v.(type) {
		case string:
			vals[k] = v
		case int64:
			vals[k] = strconv.FormatInt(v, 10)
		case bool:
			vals[k] = strconv.FormatBool(v)
		case json.Number:
			vals[k] = v.String()
		}
	}
	keys := make([]string, 0, len(vals))
	for k := range vals {
		if k != "Token" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(vals[k])
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// tbankAnswer is what Т-Банк answers to Init and GetState.
type tbankAnswer struct {
	Success    bool        `json:"Success"`
	ErrorCode  string      `json:"ErrorCode"`
	Message    string      `json:"Message"`
	Details    string      `json:"Details"`
	PaymentID  json.Number `json:"PaymentId"`
	PaymentURL string      `json:"PaymentURL"`
	Status     string      `json:"Status"`
	Amount     int64       `json:"Amount"`
}

func (s *Store) tbank(ctx context.Context, method string, fields map[string]any, password string) (tbankAnswer, error) {
	fields["Token"] = tbankToken(fields, password)
	resp, err := s.call(ctx, Request{Method: http.MethodPost, URL: TBankURL + method, JSON: fields})
	if err != nil {
		return tbankAnswer{}, err
	}
	var out tbankAnswer
	dec := json.NewDecoder(bytes.NewReader(resp.Body))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return out, httpErr(resp)
	}
	if !out.Success || out.ErrorCode != "" && out.ErrorCode != "0" {
		return out, errors.New(strings.TrimSpace(out.Message + " " + out.Details + " (" + out.ErrorCode + ")"))
	}
	return out, nil
}

// create starts the payment at the service: its id there and the page the client pays on.
func (s *Store) create(ctx context.Context, x System, secret string, p Payment, in Start) (string, string, error) {
	desc := cut(in.Description, 128)
	switch x.Kind {
	case "yookassa":
		body := map[string]any{
			"amount":       yooAmount{strconv.FormatFloat(p.Amount, 'f', 2, 64), p.Currency},
			"capture":      true,
			"confirmation": map[string]string{"type": "redirect", "return_url": in.ReturnURL},
			"description":  desc,
			"metadata":     map[string]string{"go24_payment": p.ID, "go24_invoice": p.InvoiceID},
		}
		resp, err := s.call(ctx, Request{Method: http.MethodPost, URL: YooKassaURL, JSON: body, User: x.Login, Pass: secret,
			Header: map[string]string{"Idempotence-Key": p.ID}})
		if err != nil {
			return "", "", err
		}
		var out yooPayment
		if err := json.Unmarshal(resp.Body, &out); err != nil || resp.Status/100 != 2 || out.ID == "" {
			return "", "", out.err(resp)
		}
		return out.ID, out.Confirmation.URL, nil
	case "tbank":
		fields := map[string]any{"TerminalKey": x.Login, "Amount": int64(math.Round(p.Amount * 100)), "OrderId": p.ID,
			"Description": desc, "SuccessURL": in.ReturnURL, "FailURL": in.ReturnURL}
		if in.NotifyURL != "" {
			fields["NotificationURL"] = in.NotifyURL
		}
		out, err := s.tbank(ctx, "Init", fields, secret)
		if err != nil {
			return "", "", err
		}
		return out.PaymentID.String(), out.PaymentURL, nil
	}
	return "", "", errSystem
}

// state asks the service about a payment: pending, succeeded or canceled, and the sum it is of.
func (s *Store) state(ctx context.Context, x System, secret, extID string) (string, float64, error) {
	switch x.Kind {
	case "yookassa":
		resp, err := s.call(ctx, Request{Method: http.MethodGet, URL: YooKassaURL + "/" + url.PathEscape(extID), User: x.Login, Pass: secret})
		if err != nil {
			return "", 0, err
		}
		var out yooPayment
		if err := json.Unmarshal(resp.Body, &out); err != nil || resp.Status/100 != 2 {
			return "", 0, out.err(resp)
		}
		amount, _ := strconv.ParseFloat(out.Amount.Value, 64)
		switch out.Status {
		case "succeeded":
			return "succeeded", amount, nil
		case "canceled":
			return "canceled", amount, nil
		}
		return "pending", amount, nil // pending, waiting_for_capture
	case "tbank":
		out, err := s.tbank(ctx, "GetState", map[string]any{"TerminalKey": x.Login, "PaymentId": extID}, secret)
		if err != nil {
			return "", 0, err
		}
		amount := float64(out.Amount) / 100
		switch out.Status {
		case "CONFIRMED":
			return "succeeded", amount, nil
		case "CANCELED", "REJECTED", "DEADLINE_EXPIRED", "AUTH_FAIL", "REVERSED", "REFUNDED", "PARTIAL_REFUNDED":
			return "canceled", amount, nil
		}
		return "pending", amount, nil
	}
	return "", 0, errSystem
}

// Notice reads which payment a service's notice is about (its id at the service). The notice itself is not trusted:
// the payment is checked at the service (Check). Т-Банк's notice is also signed — a wrong token is no notice.
func (s *Store) Notice(ctx context.Context, systemID string, body []byte) (string, error) {
	x, secret, err := s.system(ctx, systemID)
	if err != nil {
		return "", err
	}
	switch x.Kind {
	case "yookassa":
		var n struct {
			Object struct {
				ID string `json:"id"`
			} `json:"object"`
		}
		if err := json.Unmarshal(body, &n); err != nil || n.Object.ID == "" {
			return "", ErrNotFound
		}
		return n.Object.ID, nil
	case "tbank":
		var n map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&n); err != nil {
			return "", ErrNotFound
		}
		token, _ := n["Token"].(string)
		if token == "" || token != tbankToken(n, secret) || fmt.Sprint(n["TerminalKey"]) != x.Login {
			return "", ErrNotFound
		}
		return fmt.Sprint(n["PaymentId"]), nil
	}
	return "", errSystem
}
