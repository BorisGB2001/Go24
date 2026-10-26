package notifyout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// VK: the messages of a VK community (its access key with the «messages» and «manage» rights, «Long Poll API» on
// with the «Входящее сообщение» event). The Bots Long Poll API is asked for the events (so go24 needs no public
// address), messages.send writes the notifications. The person writes the community the code in its messages.
type VK struct {
	API  string // https://api.vk.com/method (a field for tests)
	HTTP *http.Client

	mu   sync.Mutex
	poll map[string]vkPoll // the community's token → its Long Poll server and key, kept while go24 runs
}

type vkPoll struct{ server, key string }

const vkVersion = "5.199"

func init() {
	Register(&VK{API: "https://api.vk.com/method", HTTP: &http.Client{Timeout: 20 * time.Second}})
}

func (v *VK) Name() string                   { return "vk" }
func (v *VK) NeedsServer() bool              { return false }
func (v *VK) LinkURL(b Bot, _ string) string { return "" }

// vkError is an error of the API: its code (901, 902 — the person does not let the community write) and text.
type vkError struct {
	Code    int
	Message string
}

func (e *vkError) Error() string { return "vk: " + strconv.Itoa(e.Code) + " " + e.Message }

// call asks a method; the token goes in the form, not in the address.
func (v *VK) call(ctx context.Context, b Bot, method string, args url.Values, out any) error {
	args.Set("access_token", b.Token())
	args.Set("v", vkVersion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.API+"/"+method, strings.NewReader(args.Encode()))
	if err != nil {
		return plain(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return plain(err)
	}
	defer resp.Body.Close()
	var r struct {
		Response json.RawMessage `json:"response"`
		Error    *struct {
			Code    int    `json:"error_code"`
			Message string `json:"error_msg"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("vk %s: %s", method, resp.Status)
	}
	if r.Error != nil {
		return &vkError{r.Error.Code, r.Error.Message}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Response, out)
}

type vkGroup struct {
	ID         int64  `json:"id"`
	ScreenName string `json:"screen_name"`
}

// group is the community of the token: groups.getById answers a list (older versions) or {"groups": [...]}.
func (v *VK) group(ctx context.Context, b Bot) (vkGroup, error) {
	var raw json.RawMessage
	if err := v.call(ctx, b, "groups.getById", url.Values{}, &raw); err != nil {
		return vkGroup{}, err
	}
	var list []vkGroup
	if json.Unmarshal(raw, &list) != nil {
		var r struct {
			Groups []vkGroup `json:"groups"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return vkGroup{}, err
		}
		list = r.Groups
	}
	if len(list) == 0 {
		return vkGroup{}, errors.New("vk: the key is not a community's")
	}
	return list[0], nil
}

// Check gives the community's short name (vk.com/<name>): employees write it.
func (v *VK) Check(ctx context.Context, b Bot) (string, error) {
	g, err := v.group(ctx, b)
	if err != nil {
		return "", err
	}
	if g.ScreenName == "" {
		return "club" + strconv.FormatInt(g.ID, 10), nil
	}
	return g.ScreenName, nil
}

// Updates asks the Long Poll server for the events after next (its event number); the first time only the
// number is noted: the old messages are not answered.
func (v *VK) Updates(ctx context.Context, b Bot, next int64) ([]Update, int64, error) {
	v.mu.Lock()
	p, ok := v.poll[b.Token()]
	v.mu.Unlock()
	if !ok {
		g, err := v.group(ctx, b)
		if err != nil {
			return nil, next, err
		}
		var s struct {
			Server string `json:"server"`
			Key    string `json:"key"`
			TS     string `json:"ts"`
		}
		if err := v.call(ctx, b, "groups.getLongPollServer", url.Values{"group_id": {strconv.FormatInt(g.ID, 10)}}, &s); err != nil {
			return nil, next, err
		}
		p = vkPoll{s.Server, s.Key}
		v.mu.Lock()
		if v.poll == nil {
			v.poll = map[string]vkPoll{}
		}
		v.poll[b.Token()] = p
		v.mu.Unlock()
		if next == 0 {
			ts, _ := strconv.ParseInt(s.TS, 10, 64)
			return nil, ts, nil
		}
	}
	q := url.Values{"act": {"a_check"}, "key": {p.key}, "ts": {strconv.FormatInt(next, 10)}, "wait": {"0"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.server+"?"+q.Encode(), nil)
	if err != nil {
		return nil, next, plain(err)
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return nil, next, plain(err)
	}
	defer resp.Body.Close()
	var r struct {
		TS      json.Number `json:"ts"`
		Failed  int         `json:"failed"`
		Updates []struct {
			Type   string `json:"type"`
			Object struct {
				Message struct {
					FromID int64  `json:"from_id"`
					PeerID int64  `json:"peer_id"`
					Text   string `json:"text"`
				} `json:"message"`
			} `json:"object"`
		} `json:"updates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, next, fmt.Errorf("vk long poll: %s", resp.Status)
	}
	ts, _ := r.TS.Int64()
	switch r.Failed {
	case 0:
	case 1: // the events before are lost: on from the number given
		return nil, ts, nil
	default: // the key is old: a new one the next time
		v.mu.Lock()
		delete(v.poll, b.Token())
		v.mu.Unlock()
		if r.Failed == 3 {
			return nil, 0, nil
		}
		return nil, next, nil
	}
	var out []Update
	for _, u := range r.Updates {
		m := u.Object.Message
		if u.Type != "message_new" || m.PeerID != m.FromID || m.FromID <= 0 { // a chat or a community: notifications are personal
			continue
		}
		id := strconv.FormatInt(m.FromID, 10)
		out = append(out, Update{Chat: id, Account: "vk.com/id" + id, Text: m.Text})
	}
	return out, max(ts, next), nil
}

func (v *VK) Send(ctx context.Context, b Bot, chat string, m Message) error {
	text := m.Text
	if m.Link != "" {
		text += "\n" + m.Link
	}
	err := v.call(ctx, b, "messages.send", url.Values{"peer_id": {chat}, "message": {text}, "dont_parse_links": {"1"},
		"random_id": {strconv.FormatInt(int64(rand.Int32()), 10)}}, nil)
	// 901: the person does not let the community write to them; 902: their privacy settings forbid it
	if ve, ok := err.(*vkError); ok && (ve.Code == 901 || ve.Code == 902) {
		return errors.Join(ErrGone, err)
	}
	return err
}
