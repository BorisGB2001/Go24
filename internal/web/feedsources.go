package web

import (
	"context"
	"time"

	"go24/internal/feed"
)

// The events of other sections in the feed (feed.Announce): the sections call these after their own
// work is done; a failure here is logged, it does not undo that work.

func (s *Server) announce(ctx context.Context, a feed.Announcement) {
	if s.Feed == nil {
		return
	}
	if _, err := s.Feed.Announce(ctx, a); err != nil {
		s.Log.Warn("feed announce", "source", a.Source, "id", a.SourceID, "err", err)
	}
}

func (s *Server) withdraw(ctx context.Context, source, id string) {
	if s.Feed == nil || id == "" {
		return
	}
	if err := s.Feed.Withdraw(ctx, source, id); err != nil {
		s.Log.Warn("feed withdraw", "source", source, "id", id, "err", err)
	}
}

// feedWelcome: «Приветствуем нового сотрудника» — an employee new to go24 (not a guest of collabs), to all employees.
func (s *Server) feedWelcome(ctx context.Context, userID, name string) {
	s.announce(ctx, feed.Announcement{Source: feed.SourceWelcome, SourceID: userID, AuthorID: userID, Title: name})
}

// feedMeeting: a meeting with people invited is a post of its organizer seen by them; saved again — brought up to date
// (those invited later see it too).
func (s *Server) feedMeeting(ctx context.Context, viewer, id string) {
	if s.Feed == nil || s.Calendar == nil || id == "" {
		return
	}
	e, err := s.Calendar.Get(ctx, viewer, id)
	if err != nil {
		s.Log.Warn("feed meeting", "id", id, "err", err)
		return
	}
	var to []string
	for _, a := range e.Attendees {
		if a.ID != "" && a.ID != e.OwnerID {
			to = append(to, a.ID)
		}
	}
	if len(to) == 0 || e.Kind == "task" {
		return
	}
	where := e.Where
	if e.RoomName != "" {
		where = e.RoomName
	}
	data := map[string]string{"start": e.Start.Format(time.RFC3339), "end": e.End.Format(time.RFC3339), "where": where}
	if e.AllDay {
		data["allday"] = "1"
	}
	s.announce(ctx, feed.Announcement{Source: feed.SourceMeeting, SourceID: e.ID, AuthorID: e.OwnerID, To: append(to, e.OwnerID),
		Title: e.Title, Data: data})
}

// feedKBPage: a new page of a space for all employees is a post to them, of a project's space — to the project; a
// space with «Права доступа» of its own is not in the feed.
func (s *Server) feedKBPage(ctx context.Context, userID, pageID string) {
	if s.Feed == nil || s.KB == nil {
		return
	}
	p, sp, err := s.KB.Page(ctx, userID, pageID)
	if err != nil || sp.Restricted {
		return
	}
	a := feed.Announcement{Source: feed.SourceKB, SourceID: p.ID, AuthorID: userID, Title: p.Title,
		Data: map[string]string{"space": sp.ID, "space_name": sp.Name}}
	if sp.ProjectKey != "" {
		pr, err := s.Tasks.ProjectByKey(ctx, userID, sp.ProjectKey)
		if err != nil {
			return
		}
		a.ProjectID = pr.ID
	}
	s.announce(ctx, a)
}
