package telephony

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"
)

// «Текущие звонки» (as a supervisor's call monitoring): the conversations of the employees going on now, read from
// Asterisk, and listening to one of them — go24 rings the supervisor's phones, the one who answers is put on ChanSpy
// of the employee's channel (go24-spy of the dialplan): to listen, to whisper (only the employee hears) or to barge
// in (both hear); the keys 4, 5 and 6 of the phone switch between them.

// SpyModes are the ways of joining a conversation.
var SpyModes = []string{"listen", "whisper", "barge"}

// Active is a call of an employee going on now: their channel in Asterisk.
type Active struct {
	Channel  string    // the name of the employee's channel (PJSIP/w101-0000001a): what ChanSpy listens to
	UserID   string    // the employee
	Name     string    // their name
	Number   string    // their inner number
	Peer     string    // the number they talk to
	PeerName string    // its name, if Asterisk knows it
	Out      bool      // the employee called (else — they were called)
	Up       bool      // they talk (else — it rings)
	Since    time.Time // since when the channel exists
}

// Seconds the call goes on by now.
func (a Active) Seconds(now time.Time) int { return max(0, int(now.Sub(a.Since).Seconds())) }

// ariChannel is a channel as ARI gives it.
type ariChannel struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Caller struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	} `json:"caller"`
	Connected struct {
		Name   string `json:"name"`
		Number string `json:"number"`
	} `json:"connected"`
	Created  string `json:"creationtime"`
	Dialplan struct {
		App string `json:"app_name"`
	} `json:"dialplan"`
}

// channels are the ones Asterisk has now.
func (a *ARI) channels(ctx context.Context) ([]ariChannel, error) {
	var out []ariChannel
	err := a.do(ctx, http.MethodGet, "/channels", &out)
	return out, err
}

// Spy rings the supervisor's phones (their inner number); the one who answers joins the channel in the mode.
func (a *ARI) Spy(ctx context.Context, inner, channel, mode string) error {
	v := url.Values{"endpoint": {"Local/" + inner + "@go24-call/n"}, "extension": {"s"}, "context": {"go24-spy"},
		"priority": {"1"}, "callerId": {inner}, "timeout": {"45"}}
	body := map[string]any{"variables": map[string]string{"GO24_SPY": channel, "GO24_SPYMODE": mode}}
	return a.send(ctx, http.MethodPost, "/channels?"+v.Encode(), body, nil)
}

// Active are the calls of the employees going on now (users: of these only, nil — everyone), the longest first.
func (p *PBX) Active(ctx context.Context, users []string) ([]Active, error) {
	if !p.Ready() {
		return nil, ErrOff
	}
	chans, err := p.ARI.channels(ctx)
	if err != nil {
		return nil, err
	}
	lines, err := p.Lines(ctx)
	if err != nil {
		return nil, err
	}
	return activeOf(chans, lines, users), nil
}

// activeOf picks the channels of the employees' phones and softphones out of Asterisk's.
func activeOf(chans []ariChannel, lines []Line, users []string) []Active {
	by := map[string]Line{}
	for _, l := range lines {
		if !l.Dup {
			by[l.Login()], by[l.WebLogin()] = l, l
		}
	}
	var out []Active
	for _, c := range chans {
		tech, rest, ok := strings.Cut(c.Name, "/")
		if !ok || tech != "PJSIP" {
			continue
		}
		login, _, _ := strings.Cut(rest, "-")
		l, ok := by[login]
		if !ok || (users != nil && !slices.Contains(users, l.UserID)) {
			continue
		}
		a := Active{Channel: c.Name, UserID: l.UserID, Name: l.Name, Number: l.Number, Up: c.State == "Up",
			Out: c.Dialplan.App != "AppDial" && c.Dialplan.App != "AppQueue"}
		switch c.State {
		case "Up", "Ring", "Ringing":
		default:
			continue // being made or ending
		}
		if a.Out {
			a.Peer, a.PeerName = c.Connected.Number, c.Connected.Name
		} else {
			a.Peer, a.PeerName = c.Caller.Number, c.Caller.Name
			if a.Peer == "" || a.Peer == l.Number {
				a.Peer, a.PeerName = c.Connected.Number, c.Connected.Name
			}
		}
		if a.PeerName == a.Peer {
			a.PeerName = ""
		}
		a.Since, _ = time.Parse("2006-01-02T15:04:05.000-0700", c.Created)
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Since.Before(out[j].Since) })
	return out
}
