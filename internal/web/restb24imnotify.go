package web

import (
	"net/http"
	"strconv"
	"strings"

	"go24/internal/notify"
)

// REST compatible with the portal, the messenger part 6 (GO24DEV-937): im.notify.read.list — several notifications read
// or unread; im.notify.schema.get — the kinds of notifications by module, as the settings page has them;
// im.smile.get — the smileys of the messenger's choice.

func init() {
	for name, m := range map[string]portalMethod{
		"im.notify.read.list":  {Scope: "im:read", Run: (*Server).portalNotifyReadList},
		"im.notify.schema.get": {Scope: "im:read", Run: (*Server).portalNotifySchema},
		"im.smile.get":         {Scope: "im:read", Run: (*Server).portalSmiles},
	} {
		portalMethods[strings.ToLower(name)] = m
	}
}

// im.notify.read.list: IDS — the caller's notifications, ACTION — Y read (by default), N unread → true.
func (s *Server) portalNotifyReadList(c portalCall) (portalOut, error) {
	var ids []int64
	for _, v := range portalList(portalParam(c.in, "IDS")) {
		if id, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "PARAMS_ERROR", "err.portal_field", "IDS")
	}
	q := `UPDATE notifications SET read_at = now() WHERE user_id = $1 AND id = ANY($2) AND read_at IS NULL`
	if strings.ToUpper(portalStr(portalParam(c.in, "ACTION"))) == "N" {
		q = `UPDATE notifications SET read_at = NULL WHERE user_id = $1 AND id = ANY($2)`
	}
	_, err := s.DB.Exec(c.r.Context(), q, c.p.UserID, ids)
	return portalOut{Result: err == nil}, err
}

// portalSchemaModules: the module a section of the notification settings answers to (the reverse of
// portalNotifyModules).
var portalSchemaModules = map[string]string{"messenger": "im", "projects": "socialnetwork", "feed": "blog",
	"telephony": "voximplant", "apps": "rest", "company": "intranet", "booking": "landing"}

// im.notify.schema.get: the kinds of notifications by module — {module: {NAME, MODULE_ID, NOTIFY: {kind: {NAME, SITE,
// MAIL, PUSH, DISABLED}}}}; SITE and PUSH are whether the caller gets the kind (their settings), MAIL is always off:
// go24 sends no notifications by e-mail.
func (s *Server) portalNotifySchema(c portalCall) (portalOut, error) {
	set, err := s.Notify.Settings(c.r.Context(), c.p.UserID)
	if err != nil {
		return portalOut{}, err
	}
	out := map[string]any{}
	for _, g := range notify.Kinds {
		module := portalSchemaModules[g.Group]
		if module == "" {
			module = g.Group
		}
		kinds := map[string]any{}
		if m, ok := out[module].(map[string]any); ok {
			kinds = m["NOTIFY"].(map[string]any)
		} else {
			out[module] = map[string]any{"MODULE_ID": module, "NAME": s.tr(c.r, "notify.group."+g.Group), "NOTIFY": kinds}
		}
		for _, k := range g.Kinds {
			on := !set.Off[k]
			kinds[k] = map[string]any{"MODULE_ID": module, "NAME": s.tr(c.r, "notify.set."+k), "SITE": on, "MAIL": false,
				"PUSH": on, "DISABLED": []string{"MAIL"}}
		}
	}
	return portalOut{Result: out}, nil
}

// portalSmileSets are the smileys of the messenger's choice — the same as EMOJI of im.js.
var portalSmileSets = []struct{ Name, Smileys string }{
	{"smileys", "😀 😃 😄 😁 😆 😅 😂 🤣 😊 😇 🙂 🙃 😉 😌 😍 🥰 😘 😋 😛 😜 🤪 🤨 🧐 🤓 😎 🥳 😏 😒 😞 😔 😟 🙁 😣 😫 😩 🥺 😢 😭 😤 😠 😡 🤯 😳 😱 😨 😰 😓 🤗 🤔 🤭 🤫 😶 😐 😑 😬 🙄 😮 😲 🥱 😴 🤤 😵 🤐 🥴 🤢 🤧 😷 🤒 🤕"},
	{"gestures", "👍 👎 👌 ✌️ 🤞 🤟 🤘 🤙 👈 👉 👆 👇 ☝️ ✋ 🖐️ 👋 🤝 🙏 💪 👏 🙌 👐 ✍️"},
	{"hearts", "❤️ 🧡 💛 💚 💙 💜 🖤 🤍 🤎 💔 ❣️ 💕 💞 💓 💗 💖 💘 💝"},
	{"objects", "🎉 🎊 🎁 🏆 🥇 🔥 ⭐ 🌟 ✨ ⚡ 💡 📌 📎 📅 📈 📉 ✅ ❌ ⚠️ ❓ ❗ 💯 🚀 ☕ 🍕 🍰 🎂"},
}

// im.smile.get: the smileys — SETS of the choice and SMILE, each a Unicode one: its TYPING is the character itself
// and it has no IMAGE; go24 has no smileys of pictures.
func (s *Server) portalSmiles(c portalCall) (portalOut, error) {
	sets, smiles := []map[string]any{}, []map[string]any{}
	for i, set := range portalSmileSets {
		sets = append(sets, map[string]any{"ID": i + 1, "NAME": s.tr(c.r, "im.emoji_"+set.Name), "TYPE": "G"})
		for _, e := range strings.Fields(set.Smileys) {
			smiles = append(smiles, map[string]any{"ID": len(smiles) + 1, "SET_ID": i + 1, "NAME": e, "TYPING": e,
				"IMAGE": "", "WIDTH": 0, "HEIGHT": 0})
		}
	}
	return portalOut{Result: map[string]any{"SETS": sets, "SMILE": smiles}}, nil
}
