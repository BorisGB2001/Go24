package web

import (
	"context"
	"net/http"
	"strings"

	"go24/internal/auth"
	"go24/internal/i18n"
	"go24/internal/mail"
)

// mailFav is a place of «Избранное» in the folder pane: one of the user's folders, categories, saved searches or a
// virtual folder (flagged, snoozed, all inboxes — the name is the template's).
type mailFav struct {
	Ref     string
	Folder  *mail.Folder
	Cat     *mail.Category
	Search  *mail.Search
	Virtual bool
	Box     string // the folder's mailbox, when there are several
}

// mailFavs is the user's «Избранное», as the folder pane shows it: refs to what is gone (a deleted folder, category,
// search) are left out. The defaults are Outlook's: Inbox, Sent, Drafts of the first mailbox.
func (s *Server) mailFavs(ctx context.Context, userID string, boxes []mailBox, cats []mail.Category, searches []mail.Search) ([]mailFav, map[string]bool, error) {
	var defaults []string
	if len(boxes) > 0 {
		for _, role := range []string{"inbox", "sent", "drafts"} {
			for _, f := range boxes[0].Folders {
				if f.Role == role {
					defaults = append(defaults, f.ID)
					break
				}
			}
		}
	}
	refs, err := s.Mail.Favorites(ctx, userID, defaults...)
	if err != nil {
		return nil, nil, err
	}
	var list []mailFav
	set := map[string]bool{}
	for _, ref := range refs {
		fav := mailFav{Ref: ref}
		switch {
		case ref == mail.FlaggedFolder || ref == mail.SnoozedFolder || ref == mail.AllInboxes:
			fav.Virtual = len(boxes) > 0 && (ref != mail.AllInboxes || len(boxes) > 1)
		case strings.HasPrefix(ref, mail.CategoryFolder):
			for i := range cats {
				if cats[i].ID == ref[len(mail.CategoryFolder):] {
					fav.Cat = &cats[i]
				}
			}
		case strings.HasPrefix(ref, mail.SearchFolder):
			for i := range searches {
				if searches[i].ID == ref[len(mail.SearchFolder):] {
					fav.Search = &searches[i]
				}
			}
		default:
			for _, b := range boxes {
				for i := range b.Folders {
					if b.Folders[i].ID == ref && b.Folders[i].Selectable {
						fav.Folder = &b.Folders[i]
						if len(boxes) > 1 {
							fav.Box = b.Email
						}
					}
				}
			}
		}
		if fav.Virtual || fav.Folder != nil || fav.Cat != nil || fav.Search != nil {
			list = append(list, fav)
			set[ref] = true
		}
	}
	return list, set, nil
}

// mailFavSet is «Добавить в избранное» (on=1) and «Удалить из избранного» of a folder pane's menu.
func (s *Server) mailFavSet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ref := r.FormValue("ref")
	if ref == "" || len(ref) > 100 {
		s.mailErr(w, r, i18n.E("err.bad_form"), "/mail")
		return
	}
	if err := s.Mail.Favorite(r.Context(), sess.UserID, ref, r.FormValue("on") == "1"); err != nil {
		s.mailErr(w, r, err, mailBack(r))
		return
	}
	http.Redirect(w, r, mailBack(r), http.StatusSeeOther)
}

// mailFavMove is «Выше» / «Ниже» (dir=up|down) of a place in «Избранное», and its new order by dragging (order=ref,ref…).
func (s *Server) mailFavMove(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error
	if order := r.FormValue("order"); order != "" {
		err = s.Mail.SetFavorites(r.Context(), sess.UserID, strings.Split(order, ","))
	} else {
		by := 1
		if r.FormValue("dir") == "up" {
			by = -1
		}
		err = s.Mail.MoveFavorite(r.Context(), sess.UserID, r.FormValue("ref"), by)
	}
	if err != nil {
		s.mailErr(w, r, err, mailBack(r))
		return
	}
	if fromForm(r) || wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{"result": true})
		return
	}
	http.Redirect(w, r, mailBack(r), http.StatusSeeOther)
}
