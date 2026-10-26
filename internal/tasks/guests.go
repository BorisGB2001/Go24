package tasks

import (
	"context"
)

// Guests (users.guest) are people from outside invited into collabs. A guest sees only their collabs — their chats,
// tasks, drives, calendars and members; the company (its structure, feed, employees, CRM, other chats) does not exist
// for them. People meet through collabs: a guest knows only the people of the collabs they are in, and an employee
// knows a guest only through a collab they share (the portal shows guests the same way).

type viewerKey struct{}

type viewer struct {
	ID    string
	Guest bool
}

// WithViewer says who makes the request (a guest or not): lists of people then show only the people the viewer may
// know (see PeerSQL). The web server sets it for every signed-in request.
func WithViewer(ctx context.Context, userID string, guest bool) context.Context {
	return context.WithValue(ctx, viewerKey{}, viewer{userID, guest})
}

// Viewer is the person the request is made by ("" — the system: a worker, a robot).
func Viewer(ctx context.Context) string { v, _ := ctx.Value(viewerKey{}).(viewer); return v.ID }

// IsGuestCtx reports whether the request is made by a guest.
func IsGuestCtx(ctx context.Context) bool { v, _ := ctx.Value(viewerKey{}).(viewer); return v.Guest }

// PeerSQL is the single rule «viewer v may know person u»: oneself; anyone, when neither is a guest; otherwise only
// when they share a collab. v is an SQL placeholder (a user id), u the alias of a users row.
func PeerSQL(v, u string) string {
	return `(` + u + `.id = (` + v + `)::uuid
		OR (NOT ` + u + `.guest AND NOT EXISTS (SELECT 1 FROM users gv WHERE gv.id = (` + v + `)::uuid AND gv.guest))
		OR EXISTS (SELECT 1 FROM project_members pa JOIN project_members pb ON pb.project_id = pa.project_id
			JOIN projects pc ON pc.id = pa.project_id
			WHERE pc.kind = 'collab' AND pa.user_id = (` + v + `)::uuid AND pb.user_id = ` + u + `.id))`
}

// GuestSQL: the user v is a guest (v is an SQL placeholder).
func GuestSQL(v string) string {
	return `EXISTS (SELECT 1 FROM users gu WHERE gu.id = (` + v + `)::uuid AND gu.guest)`
}

// KnowsPerson: the viewer may see the person (their card, photo, a private chat with them); see PeerSQL.
func (s *Store) KnowsPerson(ctx context.Context, viewerID, personID string) (bool, error) {
	if viewerID == personID {
		return true, nil
	}
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users u WHERE u.id::text = $2 AND `+PeerSQL("$1", "u")+`)`,
		viewerID, personID).Scan(&ok)
	return ok, err
}
