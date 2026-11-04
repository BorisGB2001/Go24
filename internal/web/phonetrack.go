package web

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
)

// ---------- «Колл-трекинг»: the script of a client's site ----------
//
// An external line is the number of an advertising channel (Маркетинг → «Рекламные источники»; the line's form). The
// site puts <script src=".../ct.js" async></script> on its pages: a visitor the channel brought (its utm_source, or the
// site they came from) sees the line's number instead of the company's; a new lead of a call to the line gets the
// channel's «Источник» and utm_source (phoneToCRM).

//go:embed widget/calltrack.js
var callTrackJS string

// callTrackLine is a line of the script's config.
type callTrackLine struct {
	Number   string   `json:"number"`
	Text     string   `json:"text"` // how the page shows it
	Channels []string `json:"channels"`
}

// callTrackScript is /ct.js: the script with the company's numbers and the channels' lines in it.
func (s *Server) callTrackScript(w http.ResponseWriter, r *http.Request) {
	cfg := map[string]any{"numbers": []string{}, "lines": []callTrackLine{}}
	if s.Telephony != nil && s.Marketing != nil {
		ctx := r.Context()
		trunks, err := s.Telephony.Trunks(ctx)
		if err != nil {
			s.fail(w, "call tracking", err)
			return
		}
		ads, err := s.Marketing.AdSources(ctx)
		if err != nil {
			s.fail(w, "call tracking", err)
			return
		}
		utm := map[string][]string{}
		for _, a := range ads {
			for _, u := range a.UTM {
				if u = strings.ToLower(strings.TrimSpace(u)); u != "" {
					utm[a.ID] = append(utm[a.ID], u)
				}
			}
		}
		numbers, lines := []string{}, []callTrackLine{}
		for _, t := range trunks {
			numbers = append(numbers, t.Number)
			if ch := utm[t.AdSource]; t.AdSource != "" && len(ch) > 0 {
				lines = append(lines, callTrackLine{Number: t.Number, Text: phoneText(t.Number), Channels: ch})
			}
		}
		cfg = map[string]any{"numbers": numbers, "lines": lines}
	}
	b, _ := json.Marshal(cfg)
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write([]byte(strings.Replace(callTrackJS, "/*CONFIG*/{}", string(b), 1)))
}

// phoneText writes a number as people read it: +7 495 123-45-67 for a Russian one, as it is otherwise.
func phoneText(n string) string {
	d := strings.TrimPrefix(n, "+")
	switch {
	case len(d) == 11 && d[0] == '7':
		return "+7 " + d[1:4] + " " + d[4:7] + "-" + d[7:9] + "-" + d[9:]
	case len(d) == 10 && d[0] == '9':
		return "+7 " + d[:3] + " " + d[3:6] + "-" + d[6:8] + "-" + d[8:]
	}
	return n
}
