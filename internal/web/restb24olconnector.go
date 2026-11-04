package web

import (
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"go24/internal/openlines"
)

// REST compatible with the portal, the channels of applications in the open lines (GO24DEV-989): imconnector.register,
// unregister, list — an application's own way into the lines; imconnector.activate, status, connector.data.set — on a
// line (CONNECTOR, LINE; administrators); imconnector.send.messages — the clients' messages become the line's
// dialogues, update.messages and delete.messages change them, send.status.reading — the client has read the answers;
// imconnector.chat.name.set names a client's chat. The answers go to the application as ONIMCONNECTORMESSAGEADD.

func init() {
	for name, m := range map[string]portalMethod{
		"imconnector.register":             {Scope: "openlines:write", Run: (*Server).portalConnRegister},
		"imconnector.unregister":           {Scope: "openlines:write", Run: (*Server).portalConnUnregister},
		"imconnector.list":                 {Scope: "openlines:read", Run: (*Server).portalConnList},
		"imconnector.activate":             {Scope: "openlines:write", Run: (*Server).portalConnActivate},
		"imconnector.status":               {Scope: "openlines:read", Run: (*Server).portalConnStatus},
		"imconnector.connector.data.set":   {Scope: "openlines:write", Run: (*Server).portalConnData},
		"imconnector.send.messages":        {Scope: "openlines:write", Run: (*Server).portalConnSend},
		"imconnector.update.messages":      {Scope: "openlines:write", Run: (*Server).portalConnUpdate},
		"imconnector.delete.messages":      {Scope: "openlines:write", Run: (*Server).portalConnDelete},
		"imconnector.send.status.delivery": {Scope: "openlines:write", Run: (*Server).portalConnDelivery},
		"imconnector.send.status.reading":  {Scope: "openlines:write", Run: (*Server).portalConnReading},
		"imconnector.chat.name.set":        {Scope: "openlines:write", Run: (*Server).portalConnChatName},
	} {
		portalMethods[name] = m
	}
}

// portalRows is a list of objects: a JSON array, or {"0": {...}, "1": {...}} of a PHP form.
func portalRows(v any) []map[string]any {
	switch x := v.(type) {
	case []any:
		var out []map[string]any
		for _, y := range x {
			if m := portalMap(y); m != nil {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		keys := slices.SortedFunc(maps.Keys(x), func(a, b string) int {
			na, _ := strconv.Atoi(a)
			nb, _ := strconv.Atoi(b)
			return na - nb
		})
		var out []map[string]any
		for _, k := range keys {
			if m := portalMap(x[k]); m != nil {
				out = append(out, m)
			}
		}
		return out
	}
	return nil
}

// portalConnOf is the caller's application's connector of CONNECTOR (or ID) and the line of LINE (line "" — none
// asked); admin — the caller must be an administrator.
func (s *Server) portalConnOf(c portalCall, admin, line bool) (openlines.Connector, string, error) {
	a, err := s.portalAppOf(c, admin)
	if err != nil {
		return openlines.Connector{}, "", err
	}
	code := portalStr(portalParam(c.in, "connector"))
	if code == "" {
		code = portalStr(portalParam(c.in, "id"))
	}
	if strings.TrimSpace(code) == "" {
		return openlines.Connector{}, "", s.portalErr(c.r, http.StatusBadRequest, "CONNECTOR_ID_EMPTY", "err.ol_connector_code")
	}
	conn, err := s.OpenLines.AppConnector(c.r.Context(), a.ID, code)
	if err == openlines.ErrNotFound {
		return conn, "", s.portalErr(c.r, http.StatusNotFound, "CONNECTOR_NOT_FOUND", "err.ol_connector_none")
	}
	if err != nil || !line {
		return conn, "", err
	}
	id, err := s.portalOLLineID(c, portalParam(c.in, "line"))
	if err == nil && id == "" {
		err = s.portalErr(c.r, http.StatusNotFound, "LINE_NOT_FOUND", "err.not_found")
	}
	return conn, id, err
}

func (s *Server) portalConnRegister(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	icon := portalMap(portalParam(c.in, "icon"))
	_, err = s.OpenLines.RegisterConnector(c.r.Context(), a.ID, openlines.Connector{Code: portalStr(portalParam(c.in, "id")),
		Name: portalStr(portalParam(c.in, "name")), Icon: portalStr(portalParam(icon, "data_image")), Placement: portalStr(portalParam(c.in, "placement_handler"))})
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

func (s *Server) portalConnUnregister(c portalCall) (portalOut, error) {
	a, err := s.portalAppOf(c, true)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.OpenLines.UnregisterConnector(c.r.Context(), a.ID, portalStr(portalParam(c.in, "id"))); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// imconnector.list: every channel of the lines, {code: name} — go24's own and the applications'.
func (s *Server) portalConnList(c portalCall) (portalOut, error) {
	if _, err := s.portalAppOf(c, false); err != nil {
		return portalOut{}, err
	}
	out := map[string]string{}
	for _, b := range portalOLConnectors {
		out[b[0]] = b[1]
	}
	list, err := s.OpenLines.Connectors(c.r.Context())
	if err != nil {
		return portalOut{}, err
	}
	for _, x := range list {
		out[x.Code] = x.Name
	}
	return portalOut{Result: out}, nil
}

func (s *Server) portalConnActivate(c portalCall) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, true, true)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.OpenLines.ActivateConnector(c.r.Context(), conn, line, portalYes(c.in, "active", true), s.BaseURL); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// imconnector.status: {LINE, CONNECTOR, ERROR, CONFIGURED, STATUS}.
func (s *Server) portalConnStatus(c portalCall) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, false, true)
	if err != nil {
		return portalOut{}, err
	}
	l, err := s.OpenLines.ConnectorLine(c.r.Context(), conn, line)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"LINE": s.portalOLNum(c.r.Context(), "ol_widgets", line), "CONNECTOR": conn.Code,
		"ERROR": l.Error != "", "CONFIGURED": l.Active, "STATUS": l.Active && l.Error == ""}}, nil
}

func (s *Server) portalConnData(c portalCall) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, true, true)
	if err != nil {
		return portalOut{}, err
	}
	data := map[string]any{}
	for k, v := range portalMap(portalParam(c.in, "data")) {
		data[strings.ToLower(k)] = v
	}
	if err := s.OpenLines.SetConnectorData(c.r.Context(), conn, line, data); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// portalConnMessages are MESSAGES: each {user, message, chat} (or {im, message, chat} for the statuses).
func (s *Server) portalConnMessages(c portalCall) ([]map[string]any, error) {
	list := portalRows(portalParam(c.in, "messages"))
	if len(list) == 0 {
		return nil, s.portalErr(c.r, http.StatusBadRequest, "MESSAGES_EMPTY", "err.portal_message")
	}
	if len(list) > 100 {
		list = list[:100]
	}
	return list, nil
}

// imconnector.send.messages: {SUCCESS, DATA: {RESULT: [{SUCCESS, user, chat: {id}, message: [id], session: {ID, CHAT_ID}}]}}
// — a message that did not go has SUCCESS false and its ERROR.
func (s *Server) portalConnSend(c portalCall) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, false, true)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalConnMessages(c)
	if err != nil {
		return portalOut{}, err
	}
	results, all := []map[string]any{}, true
	for _, x := range list {
		u, m, ch := portalMap(portalParam(x, "user")), portalMap(portalParam(x, "message")), portalMap(portalParam(x, "chat"))
		in := openlines.ConnectorMessage{
			User: openlines.ConnectorUser{ID: portalStr(portalParam(u, "id")), Name: portalStr(portalParam(u, "name")), LastName: portalStr(portalParam(u, "last_name")),
				Email: portalStr(portalParam(u, "email")), Phone: portalStr(portalParam(u, "phone")), URL: portalStr(portalParam(u, "url"))},
			ChatID: portalStr(portalParam(ch, "id")), ChatName: portalStr(portalParam(ch, "name")), ChatURL: portalStr(portalParam(ch, "url")),
			MessageID: portalStr(portalParam(m, "id")), Text: portalStr(portalParam(m, "text")),
		}
		for _, f := range portalRows(portalParam(m, "files")) {
			in.Files = append(in.Files, openlines.ConnectorFile{URL: portalStr(portalParam(f, "url")), Name: portalStr(portalParam(f, "name"))})
		}
		sent, err := s.OpenLines.ConnectorReceive(c.r.Context(), conn, line, in)
		if err != nil {
			all = false
			results = append(results, map[string]any{"SUCCESS": false, "ERROR": s.portalErrorOf(c.r, err).Desc, "chat": map[string]any{"id": in.ChatID}})
			continue
		}
		msgs := []int64{}
		if sent.Message > 0 {
			msgs = append(msgs, sent.Message)
		}
		results = append(results, map[string]any{"SUCCESS": true, "user": in.User.ID, "chat": map[string]any{"id": sent.Num},
			"message": msgs, "session": map[string]any{"ID": sent.Num, "CHAT_ID": sent.Num}})
	}
	return portalOut{Result: map[string]any{"SUCCESS": all, "DATA": map[string]any{"RESULT": results}}}, nil
}

// portalConnEdit is imconnector.update.messages and delete.messages: each message — {message: {id, text}, chat: {id}}.
func (s *Server) portalConnEdit(c portalCall, deleted bool) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, false, true)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalConnMessages(c)
	if err != nil {
		return portalOut{}, err
	}
	results, all := []map[string]any{}, true
	for _, x := range list {
		m, ch := portalMap(portalParam(x, "message")), portalMap(portalParam(x, "chat"))
		ids := portalList(portalParam(m, "id"))
		if len(ids) == 0 {
			ids = []string{portalStr(portalParam(m, "id"))}
		}
		ok := true
		for _, id := range ids {
			found, err := s.OpenLines.ConnectorEdit(c.r.Context(), conn, line, portalStr(portalParam(ch, "id")), id, portalStr(portalParam(m, "text")), deleted)
			if err != nil {
				return portalOut{}, err
			}
			ok = ok && found
		}
		all = all && ok
		results = append(results, map[string]any{"SUCCESS": ok})
	}
	return portalOut{Result: map[string]any{"SUCCESS": all, "DATA": map[string]any{"RESULT": results}}}, nil
}

func (s *Server) portalConnUpdate(c portalCall) (portalOut, error) { return s.portalConnEdit(c, false) }

func (s *Server) portalConnDelete(c portalCall) (portalOut, error) { return s.portalConnEdit(c, true) }

// imconnector.send.status.delivery: the answers reached the client — go24 shows only what the client has read, the
// call is taken as it is.
func (s *Server) portalConnDelivery(c portalCall) (portalOut, error) {
	if _, _, err := s.portalConnOf(c, false, true); err != nil {
		return portalOut{}, err
	}
	if _, err := s.portalConnMessages(c); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"SUCCESS": true}}, nil
}

// imconnector.send.status.reading: the client has read the answers — each {im: {message_id}, chat: {id}}.
func (s *Server) portalConnReading(c portalCall) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, false, true)
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalConnMessages(c)
	if err != nil {
		return portalOut{}, err
	}
	all := true
	for _, x := range list {
		upTo, _ := strconv.ParseInt(portalStr(portalParam(portalMap(portalParam(x, "im")), "message_id")), 10, 64)
		if err := s.OpenLines.ConnectorRead(c.r.Context(), conn, line, portalStr(portalParam(portalMap(portalParam(x, "chat")), "id")), upTo); err == openlines.ErrNotFound {
			all = false
		} else if err != nil {
			return portalOut{}, err
		}
	}
	return portalOut{Result: map[string]any{"SUCCESS": all}}, nil
}

func (s *Server) portalConnChatName(c portalCall) (portalOut, error) {
	conn, line, err := s.portalConnOf(c, false, true)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.OpenLines.ConnectorChatName(c.r.Context(), conn, line, portalStr(portalParam(c.in, "chat_id")), portalStr(portalParam(c.in, "name"))); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}
