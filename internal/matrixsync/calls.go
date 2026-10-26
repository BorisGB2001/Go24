package matrixsync

import "context"

// CallLine is a call of the chat as its line shows it (go24.call of a notice by whoever started it): going on (with
// «Присоединиться»), over with its length and people, or missed.
type CallLine struct {
	State  string   // started | ended | missed
	Video  bool
	Secs   int      // how long it went on
	People []string // who took part (names), the one who started first
}

// callOf reads go24.call of a message (nil — not a call's line).
func callOf(content map[string]any) *CallLine {
	c, ok := content["go24.call"].(map[string]any)
	if !ok {
		return nil
	}
	l := &CallLine{}
	l.State, _ = c["state"].(string)
	l.Video, _ = c["video"].(bool)
	switch v := c["secs"].(type) {
	case float64:
		l.Secs = int(v)
	case int:
		l.Secs = v
	}
	switch v := c["people"].(type) {
	case []any:
		for _, p := range v {
			if n, ok := p.(string); ok {
				l.People = append(l.People, n)
			}
		}
	case []string:
		l.People = v
	}
	if l.State == "" {
		return nil
	}
	return l
}

func (l CallLine) content() map[string]any {
	return map[string]any{"state": l.State, "video": l.Video, "secs": l.Secs, "people": append([]string{}, l.People...)}
}

// SendCall posts the call's line into the chat as the employee who started it (body — the text other clients show).
func (s *Service) SendCall(ctx context.Context, userID, room, body string, l CallLine) (string, error) {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return "", err
	}
	id, err := s.M.Send(ctx, room, map[string]any{"msgtype": "m.notice", "body": body, "go24.call": l.content()}, tok)
	return id, forbidden(err)
}

// EditCall changes the call's line (its end) as the employee who started the call.
func (s *Service) EditCall(ctx context.Context, userID, room, event, body string, l CallLine) error {
	_, tok, err := s.token(ctx, userID)
	if err != nil {
		return err
	}
	nc := map[string]any{"msgtype": "m.notice", "body": body, "go24.call": l.content()}
	_, err = s.M.Send(ctx, room, map[string]any{"msgtype": "m.notice", "body": "* " + body, "m.new_content": nc,
		"m.relates_to": map[string]any{"rel_type": "m.replace", "event_id": event}}, tok)
	return forbidden(err)
}

// Mins is how long the call went on, in whole minutes (at least one).
func (l CallLine) Mins() int { return max(1, (l.Secs+30)/60) }

// By is who started the call.
func (l CallLine) By() string {
	if len(l.People) == 0 {
		return ""
	}
	return l.People[0]
}
