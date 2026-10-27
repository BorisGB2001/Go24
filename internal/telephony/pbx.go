package telephony

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AstDir is the folder of go24's configuration (PBX.Dir) as Asterisk sees it: the sounds go24 writes are played from it.
const AstDir = "/etc/asterisk/go24"

// GreetingFile is the file of a line's greeting by its checksum, without the extension (as Playback wants it).
func GreetingFile(sum string) string { return AstDir + "/greet-" + sum }

// Realm of the SIP accounts: Asterisk keeps only md5(login:realm:password), not the passwords.
const Realm = "go24"

// Render is the PJSIP configuration of the lines (deploy/asterisk/pjsip.conf includes it): an endpoint, its password
// and its registrations for the SIP phone and for the softphone (WebRTC) of each employee. record: their calls are
// recorded (the dialplan sees GO24_RECORD). A call they transfer goes to go24-transfer of the dialplan.
func Render(lines []Line, record bool) string {
	var b strings.Builder
	b.WriteString("; go24 writes this file (Settings → Telephony): do not edit it by hand\n")
	for _, l := range lines {
		if l.Dup {
			continue
		}
		cid := fmt.Sprintf("\"%s\" <%s>", callerName(l.Name), l.Number)
		vars := "set_var = TRANSFER_CONTEXT=go24-transfer\n" // a transferred call goes there
		if record {
			vars += "set_var = GO24_RECORD=1\n"
		}
		for _, acc := range []struct {
			login, password, media string
		}{
			{l.Login(), l.Password, "direct_media = no\nrtp_symmetric = yes\nforce_rport = yes\nrewrite_contact = yes\nallow = opus,g722,ulaw,alaw\n"},
			{l.WebLogin(), l.WebPassword, "webrtc = yes\ndtls_auto_generate_cert = yes\nallow = opus,ulaw,alaw\n"},
		} {
			fmt.Fprintf(&b, "\n[%[1]s]\ntype = endpoint\ncontext = go24-phones\ndisallow = all\n%[2]s%[4]sauth = %[1]s\naors = %[1]s\ncallerid = %[3]s\n",
				acc.login, acc.media, cid, vars)
			fmt.Fprintf(&b, "\n[%[1]s]\ntype = auth\nauth_type = md5\nusername = %[1]s\nrealm = %[2]s\nmd5_cred = %[3]s\n", acc.login, Realm, md5cred(acc.login, acc.password))
			fmt.Fprintf(&b, "\n[%s]\ntype = aor\nmax_contacts = 5\nremove_existing = yes\nqualify_frequency = 60\n", acc.login)
		}
	}
	return b.String()
}

func md5cred(login, password string) string {
	h := md5.Sum([]byte(login + ":" + Realm + ":" + password))
	return hex.EncodeToString(h[:])
}

// callerName is the name shown to the one called: without what would break the caller ID or the configuration.
func callerName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '"', '<', '>', ';', '\\', '\r', '\n', '[', ']':
			return -1
		}
		return r
	}, name)
	return strings.TrimSpace(name)
}

// ARI is the REST interface of Asterisk.
type ARI struct {
	URL, User, Password string // URL: http://host:8088
	HTTP                *http.Client
}

func (a *ARI) do(ctx context.Context, method, path string, out any) error {
	return a.send(ctx, method, path, nil, out)
}

// send is do with a JSON body (nil — none).
func (a *ARI) send(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.URL, "/")+"/ari"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(a.User, a.Password)
	c := a.HTTP
	if c == nil {
		c = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("asterisk %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(msg)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Call rings the employee's phones (their inner number) showing the number; the one who answers is put through to
// the number as if they dialed it themselves (go24-click, then go24-phones): an outside number goes by their lines and
// rights. record: the conversation is recorded.
func (a *ARI) Call(ctx context.Context, inner, number string, record bool) error {
	v := url.Values{"endpoint": {"Local/" + inner + "*" + number + "@go24-click/n"}, "extension": {number}, "context": {"go24-phones"},
		"priority": {"1"}, "callerId": {inner}, "timeout": {"60"}}
	var body any
	if record {
		body = map[string]any{"variables": map[string]string{"GO24_RECORD": "1"}}
	}
	return a.send(ctx, http.MethodPost, "/channels?"+v.Encode(), body, nil)
}

// Version of Asterisk (asks it: an error — it does not answer).
func (a *ARI) Version(ctx context.Context) (string, error) {
	var info struct {
		System struct {
			Version string `json:"version"`
		} `json:"system"`
	}
	err := a.do(ctx, http.MethodGet, "/asterisk/info", &info)
	return info.System.Version, err
}

// Reload makes Asterisk read the configuration of a module again.
func (a *ARI) Reload(ctx context.Context, module string) error {
	return a.do(ctx, http.MethodPut, "/asterisk/modules/"+module, nil)
}

// Online are the SIP accounts registered now (a phone or a softphone is on).
func (a *ARI) Online(ctx context.Context) (map[string]bool, error) {
	var eps []struct {
		Resource string `json:"resource"`
		State    string `json:"state"`
	}
	if err := a.do(ctx, http.MethodGet, "/endpoints/PJSIP", &eps); err != nil {
		return nil, err
	}
	on := map[string]bool{}
	for _, e := range eps {
		if e.State == "online" {
			on[e.Resource] = true
		}
	}
	return on, nil
}

// PBX keeps Asterisk's accounts the same as go24's lines: it writes their configuration into Dir (a volume shared
// with Asterisk) and has Asterisk read it again.
type PBX struct {
	*Store
	ARI     *ARI
	Dir     string
	SIPHost string // the server the employees' SIP phones register at
	Records string // the folder of the recorded conversations (a volume shared with Asterisk); "" — none
	Key     string // what Asterisk tells go24 about the calls with (ASTERISK_SECRET)

	mu       sync.Mutex
	pjsip    confFile // the accounts (go24/pjsip.conf)
	dialplan confFile // go24's part of the dialplan: the external lines (go24/extensions.conf)
	queues   confFile // the queue groups (go24/queues.conf)
	moh      confFile // their music (go24/musiconhold.conf)
	once     sync.Once
	kick     chan struct{}
}

// ErrOff: go24 has no Asterisk to work with.
var ErrOff = errors.New("telephony: no Asterisk")

// Ready tells whether go24 has an Asterisk to work with.
func (p *PBX) Ready() bool { return p != nil && p.ARI != nil && p.ARI.URL != "" && p.Dir != "" }

// Sync writes the configuration of the lines (none while telephony is off) and reloads it, if it changed.
func (p *PBX) Sync(ctx context.Context) error {
	if !p.Ready() {
		return ErrOff
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	on, err := p.Enabled(ctx)
	if err != nil {
		return err
	}
	var lines []Line
	var trunks []Trunk
	var menus []Menu
	black := map[string]bool{}
	var callbacks []Callback
	var groups []Group
	prefs := map[string]Prefs{}
	record := false
	if on {
		if lines, err = p.Lines(ctx); err != nil {
			return err
		}
		if record, err = p.Recording(ctx); err != nil {
			return err
		}
		if trunks, err = p.Trunks(ctx); err != nil {
			return err
		}
		if menus, err = p.Menus(ctx); err != nil {
			return err
		}
		if black, err = p.BlackKeys(ctx); err != nil {
			return err
		}
		if callbacks, err = p.Callbacks(ctx); err != nil {
			return err
		}
		if groups, err = p.Groups(ctx); err != nil {
			return err
		}
		if prefs, err = p.AllPrefs(ctx); err != nil {
			return err
		}
	}
	numbers := map[string]string{}
	for _, l := range lines {
		if !l.Dup {
			numbers[l.UserID] = l.Number
		}
	}
	if err := p.sounds(ctx, trunks, menus, groups); err != nil {
		return err
	}
	keys := map[string]string{}
	for _, m := range menus {
		keys[m.ID] = MenuKey(m.ID)
	}
	queues := map[string]string{}
	for _, g := range groups {
		keys[g.ID], queues[g.ID] = GroupKey(g.ID), GroupKey(g.ID)
	}
	users := make([]string, 0, len(numbers))
	for u := range numbers {
		users = append(users, u)
	}
	limits, err := p.OutLimits(ctx, users)
	if err != nil {
		return err
	}
	accounts, dialplan := RenderTrunks(trunks, numbers, keys, limits, prefs, record, time.Local.String())
	queueConf, mohConf, queuePlan := RenderGroups(groups, numbers)
	dialplan += RenderMenus(menus, numbers, queues) + queuePlan + RenderBlacklist(black)
	byID := map[string]Trunk{}
	for _, t := range trunks {
		byID[t.ID] = t
	}
	dialplan += RenderCallbacks(callbacks, byID, numbers, record)
	for _, f := range []struct {
		file   *confFile
		name   string
		text   string
		module string
	}{
		{&p.pjsip, "pjsip.conf", Render(lines, record) + accounts, "res_pjsip.so"},
		{&p.queues, "queues.conf", queueConf, "app_queue.so"},
		{&p.moh, "musiconhold.conf", mohConf, "res_musiconhold.so"},
		{&p.dialplan, "extensions.conf", dialplan, "pbx_config.so"},
	} {
		if err := f.file.sync(ctx, p.ARI, filepath.Join(p.Dir, f.name), f.text, f.module); err != nil {
			return err
		}
	}
	return nil
}

// sounds writes the beep and the greetings of the lines, the voice menus and the robots' messages the dialplan plays (files named by their
// checksums, so a written one stays as it is) and removes the greetings nobody has any more.
func (p *PBX) sounds(ctx context.Context, trunks []Trunk, menus []Menu, groups []Group) error {
	beep := filepath.Join(p.Dir, "beep.wav")
	if _, err := os.Stat(beep); errors.Is(err, os.ErrNotExist) {
		if err := writeFile(beep, Beep()); err != nil {
			return err
		}
	}
	type sound struct {
		sum, id string
		get     func(context.Context, string) ([]byte, error)
	}
	var all []sound
	for _, t := range trunks {
		all = append(all, sound{t.GreetingSum, t.ID, p.TrunkGreeting})
		all = append(all, sound{t.RateSum, t.ID, p.TrunkRatePrompt})
	}
	for _, m := range menus {
		all = append(all, sound{m.GreetingSum, m.ID, p.MenuGreeting})
	}
	for _, g := range groups {
		all = append(all, sound{g.MusicSum, g.ID, p.GroupMusic})
	}
	robots, err := p.robotSounds(ctx) // the messages of «Информационный звонок»
	if err != nil {
		return err
	}
	for _, sum := range robots {
		all = append(all, sound{sum, sum, p.Sound})
	}
	keep := map[string]bool{}
	for _, x := range all {
		if x.sum == "" {
			continue
		}
		name := filepath.Base(GreetingFile(x.sum)) + ".wav"
		keep[name] = true
		file := filepath.Join(p.Dir, name)
		if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		g, err := x.get(ctx, x.id)
		if err != nil || g == nil {
			return err
		}
		if err := writeFile(file, g); err != nil {
			return err
		}
	}
	old, _ := filepath.Glob(filepath.Join(p.Dir, "greet-*.wav"))
	for _, f := range old {
		if !keep[filepath.Base(f)] {
			os.Remove(f)
		}
	}
	return nil
}

// writeFile writes a file whole: Asterisk never reads half of it.
func writeFile(file string, data []byte) error {
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

// confFile is a file of the configuration go24 writes for Asterisk.
type confFile struct {
	written string // what is in the file now
	stale   bool   // Asterisk has not reloaded it yet
}

// sync writes the file, if it changed, and has Asterisk's module read it again.
func (c *confFile) sync(ctx context.Context, ari *ARI, file, text, module string) error {
	if c.written == "" { // after a restart of go24: what is in the file already
		if b, err := os.ReadFile(file); err == nil {
			c.written = string(b)
		}
	}
	if text != c.written {
		if err := writeFile(file, []byte(text)); err != nil {
			return err
		}
		c.written, c.stale = text, true
	}
	if !c.stale {
		return nil
	}
	if err := ari.Reload(ctx, module); err != nil {
		return err // Asterisk reads the file when it starts, or once more next time
	}
	c.stale = false
	return nil
}

// Kick asks the loop to sync now (a line or the setting changed).
func (p *PBX) Kick() {
	if !p.Ready() {
		return
	}
	select {
	case p.kicks() <- struct{}{}:
	default:
	}
}

func (p *PBX) kicks() chan struct{} {
	p.once.Do(func() { p.kick = make(chan struct{}, 1) })
	return p.kick
}

// Loop syncs the configuration when kicked and every half a minute (an employee dismissed, a number imported, Asterisk
// restarted with an old file) until ctx ends; failures go to fail.
func (p *PBX) Loop(ctx context.Context, fail func(error)) {
	if !p.Ready() {
		return
	}
	k := p.kicks()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		if err := p.Sync(ctx); err != nil && ctx.Err() == nil {
			fail(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-k:
		case <-t.C:
		}
	}
}
