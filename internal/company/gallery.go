package company

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Фотогалерея» of the company: albums, and albums in them, with photos. An album is seen by everybody
// or by the people and departments (with sub-departments) of its «Кто видит» — and by those who see the albums above
// it. Administrators and HR officers with the right «Фотогалерея» keep the gallery: make albums at the top, change and
// remove any. The author of an album adds photos to it, makes albums in it and changes it; «Добавлять фото могут все,
// кто видит альбом» lets the others add photos too. Photos have a caption, «Нравится» and comments.

// Album of the gallery; Photos and Albums count what is right in it.
type Album struct {
	ID          string    `json:"id"`
	ParentID    string    `json:"parent_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	CoverID     string    `json:"cover_id"`
	AnyoneAdds  bool      `json:"anyone_adds"`
	CreatedBy   string    `json:"created_by"`
	AuthorName  string    `json:"author_name"`
	Created     time.Time `json:"created_at"`
	Updated     time.Time `json:"updated_at"`
	Photos      int       `json:"photos"`
	Albums      int       `json:"albums"`
}

// Photo of an album; Liked — by the viewer.
type Photo struct {
	ID         string    `json:"id"`
	AlbumID    string    `json:"album_id"`
	FileKey    string    `json:"-"`
	ThumbKey   string    `json:"-"`
	FileName   string    `json:"file_name"`
	Mime       string    `json:"mime"`
	Size       int64     `json:"size"`
	Width      int       `json:"width"`
	Height     int       `json:"height"`
	Caption    string    `json:"caption"`
	CreatedBy  string    `json:"created_by"`
	AuthorName string    `json:"author_name"`
	Created    time.Time `json:"created_at"`
	Likes      int       `json:"likes"`
	Liked      bool      `json:"liked"`
	Comments   int       `json:"comments"`
}

// PhotoComment is a comment on a photo.
type PhotoComment struct {
	ID       string    `json:"id"`
	UserID   string    `json:"user_id"`
	Name     string    `json:"name"`
	PhotoKey string    `json:"-"`
	Body     string    `json:"body"`
	Created  time.Time `json:"created_at"`
}

// PhotoURL of the comment's author.
func (c PhotoComment) PhotoURL() string { return PhotoURL(c.UserID, c.PhotoKey) }

// Limits of the gallery's texts, in characters.
const (
	AlbumTitleMax    = 200
	AlbumDescMax     = 2000
	PhotoCaptionMax  = 1000
	PhotoCommentMax  = 5000
	galleryRight     = "gallery"
	galleryNotifyMax = 200 // a comment in a notification
)

// CanKeepGallery: administrators and HR officers an administrator let keep «Фотогалерея».
func (s *Store) CanKeepGallery(ctx context.Context, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	h, err := s.HROf(ctx, userID)
	return h.Can(galleryRight), err
}

// albumSeesSQL: the album a is open to the user ($N) by its own «Кто видит» — for everybody, for the user or for a
// department of theirs (where they work, head it or are a deputy, or a department above it).
func albumSeesSQL(arg string) string {
	return `(NOT EXISTS (SELECT 1 FROM gallery_access x WHERE x.album_id = a.id)
		OR EXISTS (SELECT 1 FROM gallery_access x WHERE x.album_id = a.id AND (x.user_id::text = ` + arg + ` OR x.department_id IN (
			WITH RECURSIVE up AS (SELECT d.id, d.parent_id FROM departments d WHERE d.id IN (
					SELECT department_id FROM user_departments WHERE user_id::text = ` + arg + `
					UNION SELECT id FROM departments WHERE head_id::text = ` + arg + `
					UNION SELECT department_id FROM department_deputies WHERE user_id::text = ` + arg + `)
				UNION SELECT p.id, p.parent_id FROM departments p JOIN up ON p.id = up.parent_id) SELECT id FROM up))))`
}

const albumSQL = `SELECT a.id::text, coalesce(a.parent_id::text, ''), a.title, a.description, coalesce(a.cover_id::text, ''),
		a.anyone_adds, coalesce(a.created_by::text, ''), coalesce(u.name, ''), a.created_at, a.updated_at,
		(SELECT count(*) FROM gallery_photos p WHERE p.album_id = a.id), (SELECT count(*) FROM gallery_albums c WHERE c.parent_id = a.id)
	FROM gallery_albums a LEFT JOIN users u ON u.id = a.created_by`

func scanAlbums(rows pgx.Rows, err error) ([]Album, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Album])
}

// Albums in the album parentID ("" — at the top) the viewer sees (the keepers — all), the newest first.
func (s *Store) Albums(ctx context.Context, viewer, parentID string, keeper bool) ([]Album, error) {
	return scanAlbums(s.db.Query(ctx, albumSQL+` WHERE coalesce(a.parent_id::text, '') = $2 AND ($3 OR `+albumSeesSQL("$1")+`)
		ORDER BY a.created_at DESC, a.id`, viewer, parentID, keeper))
}

// Album by its id.
func (s *Store) Album(ctx context.Context, id string) (Album, error) {
	list, err := scanAlbums(s.db.Query(ctx, albumSQL+` WHERE a.id::text = $1`, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Album{}, err
	}
	return list[0], nil
}

// AlbumPath is the album with the albums above it, the top one first.
func (s *Store) AlbumPath(ctx context.Context, id string) ([]Album, error) {
	var path []Album
	for id != "" && len(path) < 50 {
		a, err := s.Album(ctx, id)
		if err != nil {
			return nil, err
		}
		path = append([]Album{a}, path...)
		id = a.ParentID
	}
	return path, nil
}

// SeesAlbum: the viewer sees the album and every album above it (the keepers see all); ErrNotFound — no such album.
func (s *Store) SeesAlbum(ctx context.Context, viewer, id string, keeper bool) (bool, error) {
	var n, open int
	err := s.db.QueryRow(ctx, `WITH RECURSIVE up AS (SELECT id, parent_id FROM gallery_albums WHERE id::text = $2
			UNION SELECT g.id, g.parent_id FROM gallery_albums g JOIN up ON g.id = up.parent_id)
		SELECT count(*), count(*) FILTER (WHERE `+albumSeesSQL("$1")+`) FROM gallery_albums a WHERE a.id IN (SELECT id FROM up)`,
		viewer, id).Scan(&n, &open)
	if err == nil && n == 0 {
		err = ErrNotFound
	}
	return err == nil && (keeper || open == n), err
}

// CanChangeAlbum: the keepers and the album's author change it, make albums in it and remove its photos.
func (a Album) CanChange(viewer string, keeper bool) bool {
	return keeper || (viewer != "" && a.CreatedBy == viewer)
}

// CanAdd: who adds photos to the album (it is seen — the caller checks).
func (a Album) CanAdd(viewer string, keeper bool) bool {
	return a.CanChange(viewer, keeper) || a.AnyoneAdds
}

func checkAlbum(a *Album) error {
	a.Title, a.Description = strings.TrimSpace(a.Title), strings.TrimSpace(a.Description)
	if a.Title == "" || utf8.RuneCountInString(a.Title) > AlbumTitleMax || utf8.RuneCountInString(a.Description) > AlbumDescMax {
		return i18n.E("err.bad_form")
	}
	return nil
}

// SaveAlbum adds an album (a.ID "", by a.CreatedBy, in a.ParentID) or changes its title, description and
// «Добавлять фото могут все»; returns its id. The caller checks the rights.
func (s *Store) SaveAlbum(ctx context.Context, a Album) (string, error) {
	if err := checkAlbum(&a); err != nil {
		return "", err
	}
	if a.ID == "" {
		err := s.db.QueryRow(ctx, `INSERT INTO gallery_albums (parent_id, title, description, anyone_adds, created_by)
			VALUES (NULLIF($1, '')::uuid, $2, $3, $4, NULLIF($5, '')::uuid) RETURNING id::text`,
			a.ParentID, a.Title, a.Description, a.AnyoneAdds, a.CreatedBy).Scan(&a.ID)
		return a.ID, err
	}
	tag, err := s.db.Exec(ctx, `UPDATE gallery_albums SET title = $2, description = $3, anyone_adds = $4, updated_at = now()
		WHERE id::text = $1`, a.ID, a.Title, a.Description, a.AnyoneAdds)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return a.ID, err
}

// AlbumAccess is «Кто видит» of the album: people (ID) and departments («dept:ID»); none — everybody.
func (s *Store) AlbumAccess(ctx context.Context, id string) ([]DeptRef, error) {
	rows, err := s.db.Query(ctx, `SELECT coalesce(x.user_id::text, 'dept:' || x.department_id::text), coalesce(u.name, d.name, '')
		FROM gallery_access x LEFT JOIN users u ON u.id = x.user_id LEFT JOIN departments d ON d.id = x.department_id
		WHERE x.album_id::text = $1 ORDER BY x.department_id IS NULL, coalesce(d.name, u.name)`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DeptRef])
}

// SetAlbumAccess chooses who sees the album: people and departments («dept:ID»); none — everybody.
func (s *Store) SetAlbumAccess(ctx context.Context, id string, to []string) error {
	users, depts := splitTo(to)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var album string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM gallery_albums WHERE id::text = $1 FOR UPDATE`, id).Scan(&album); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gallery_access WHERE album_id = $1`, album); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO gallery_access (album_id, user_id)
		SELECT $1::uuid, u.id FROM users u WHERE u.id::text = ANY($2) ON CONFLICT DO NOTHING`, album, users); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO gallery_access (album_id, department_id)
		SELECT $1::uuid, d.id FROM departments d WHERE d.id::text = ANY($2) ON CONFLICT DO NOTHING`, album, depts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// AlbumAudience is who sees the album as the nearest «Кто видит» up from it says: people and departments
// (none — everybody); a new photos' post in the feed goes to them.
func (s *Store) AlbumAudience(ctx context.Context, id string) (users, depts []string, err error) {
	path, err := s.AlbumPath(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	for i := len(path) - 1; i >= 0; i-- {
		refs, err := s.AlbumAccess(ctx, path[i].ID)
		if err != nil {
			return nil, nil, err
		}
		if len(refs) > 0 {
			ids := make([]string, len(refs))
			for j, r := range refs {
				ids[j] = r.ID
			}
			users, depts = splitTo(ids)
			return users, depts, nil
		}
	}
	return nil, nil, nil
}

// DeleteAlbum removes the album with the albums in it and all their photos.
func (s *Store) DeleteAlbum(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM gallery_albums WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

const photoSQL = `SELECT p.id::text, p.album_id::text, p.file_key, p.thumb_key, p.file_name, p.mime, p.size, p.width, p.height,
		p.caption, coalesce(p.created_by::text, ''), coalesce(u.name, ''), p.created_at,
		(SELECT count(*) FROM gallery_likes l WHERE l.photo_id = p.id),
		EXISTS (SELECT 1 FROM gallery_likes l WHERE l.photo_id = p.id AND l.user_id::text = $1),
		(SELECT count(*) FROM gallery_comments c WHERE c.photo_id = p.id)
	FROM gallery_photos p LEFT JOIN users u ON u.id = p.created_by`

func scanPhotos(rows pgx.Rows, err error) ([]Photo, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Photo])
}

// Photos of the album in the order they were added.
func (s *Store) Photos(ctx context.Context, viewer, albumID string) ([]Photo, error) {
	return scanPhotos(s.db.Query(ctx, photoSQL+` WHERE p.album_id::text = $2 ORDER BY p.created_at, p.id`, viewer, albumID))
}

// Photo by its id.
func (s *Store) Photo(ctx context.Context, viewer, id string) (Photo, error) {
	list, err := scanPhotos(s.db.Query(ctx, photoSQL+` WHERE p.id::text = $2`, viewer, id))
	if err == nil && len(list) == 0 {
		err = ErrNotFound
	}
	if err != nil {
		return Photo{}, err
	}
	return list[0], nil
}

// AddPhoto keeps a stored photo in the album (the first one becomes its cover); returns its id.
func (s *Store) AddPhoto(ctx context.Context, p Photo) (string, error) {
	p.Caption = strings.TrimSpace(p.Caption)
	if p.FileKey == "" || p.FileName == "" || utf8.RuneCountInString(p.Caption) > PhotoCaptionMax {
		return "", i18n.E("err.bad_form")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `INSERT INTO gallery_photos (album_id, file_key, thumb_key, file_name, mime, size, width, height, caption, created_by)
		SELECT a.id, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, '')::uuid FROM gallery_albums a WHERE a.id::text = $1
		RETURNING id::text`, p.AlbumID, p.FileKey, p.ThumbKey, p.FileName, p.Mime, p.Size, p.Width, p.Height, p.Caption,
		p.CreatedBy).Scan(&p.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE gallery_albums SET cover_id = coalesce(cover_id, $2::uuid), updated_at = now()
		WHERE id::text = $1`, p.AlbumID, p.ID); err != nil {
		return "", err
	}
	return p.ID, tx.Commit(ctx)
}

// SetCaption changes the photo's caption.
func (s *Store) SetCaption(ctx context.Context, id, caption string) error {
	caption = strings.TrimSpace(caption)
	if utf8.RuneCountInString(caption) > PhotoCaptionMax {
		return i18n.E("err.bad_form")
	}
	tag, err := s.db.Exec(ctx, `UPDATE gallery_photos SET caption = $2 WHERE id::text = $1`, id, caption)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// SetCover makes the photo its album's cover.
func (s *Store) SetCover(ctx context.Context, photoID string) error {
	tag, err := s.db.Exec(ctx, `UPDATE gallery_albums a SET cover_id = p.id FROM gallery_photos p
		WHERE p.id::text = $1 AND a.id = p.album_id`, photoID)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}

// MovePhoto moves the photo to another album; the old album's cover, if it was the photo, is the next photo there.
func (s *Store) MovePhoto(ctx context.Context, id, albumID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var from string
	err = tx.QueryRow(ctx, `UPDATE gallery_photos p SET album_id = a.id FROM gallery_albums a, gallery_photos o
		WHERE p.id::text = $1 AND a.id::text = $2 AND o.id = p.id RETURNING o.album_id::text`, id, albumID).Scan(&from)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := fixCover(ctx, tx, from); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE gallery_albums SET cover_id = coalesce(cover_id, $2::uuid), updated_at = now()
		WHERE id::text = $1`, albumID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// fixCover gives the album a cover again when its cover has left it: its first photo.
func fixCover(ctx context.Context, tx pgx.Tx, albumID string) error {
	_, err := tx.Exec(ctx, `UPDATE gallery_albums a SET cover_id = (SELECT p.id FROM gallery_photos p WHERE p.album_id = a.id
			ORDER BY p.created_at, p.id LIMIT 1)
		WHERE a.id::text = $1 AND (a.cover_id IS NULL OR NOT EXISTS (SELECT 1 FROM gallery_photos p WHERE p.id = a.cover_id AND p.album_id = a.id))`,
		albumID)
	return err
}

// DeletePhoto removes the photo; the album's cover, if it was the photo, is its first photo then.
func (s *Store) DeletePhoto(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var album string
	err = tx.QueryRow(ctx, `DELETE FROM gallery_photos WHERE id::text = $1 RETURNING album_id::text`, id).Scan(&album)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := fixCover(ctx, tx, album); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Like turns the user's «Нравится» of the photo on or off; returns whether it is on now and the photo's likes.
func (s *Store) Like(ctx context.Context, photoID, userID string) (bool, int, error) {
	tag, err := s.db.Exec(ctx, `DELETE FROM gallery_likes WHERE photo_id::text = $1 AND user_id::text = $2`, photoID, userID)
	if err != nil {
		return false, 0, err
	}
	on := tag.RowsAffected() == 0
	if on {
		if _, err := s.db.Exec(ctx, `INSERT INTO gallery_likes (photo_id, user_id) SELECT p.id, $2::uuid FROM gallery_photos p
			WHERE p.id::text = $1 ON CONFLICT DO NOTHING`, photoID, userID); err != nil {
			return false, 0, err
		}
	}
	var n int
	err = s.db.QueryRow(ctx, `SELECT count(*) FROM gallery_likes WHERE photo_id::text = $1`, photoID).Scan(&n)
	return on, n, err
}

// Likers are the names of those who like the photo, the latest first.
func (s *Store) Likers(ctx context.Context, photoID string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name, u.position, u.photo_key FROM gallery_likes l JOIN users u ON u.id = l.user_id
		WHERE l.photo_id::text = $1 ORDER BY l.created_at DESC`, photoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}

// PhotoComments of the photo, the oldest first.
func (s *Store) PhotoComments(ctx context.Context, photoID string) ([]PhotoComment, error) {
	rows, err := s.db.Query(ctx, `SELECT c.id::text, coalesce(c.user_id::text, ''), coalesce(u.name, ''), coalesce(u.photo_key, ''),
			c.body, c.created_at
		FROM gallery_comments c LEFT JOIN users u ON u.id = c.user_id WHERE c.photo_id::text = $1 ORDER BY c.created_at, c.id`, photoID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[PhotoComment])
}

// AddPhotoComment comments the photo; its author (not the commenter) is told.
func (s *Store) AddPhotoComment(ctx context.Context, photoID, userID, body string) (string, error) {
	body = strings.TrimSpace(body)
	if body == "" || utf8.RuneCountInString(body) > PhotoCommentMax {
		return "", i18n.E("err.bad_form")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var id, author string
	err = tx.QueryRow(ctx, `INSERT INTO gallery_comments (photo_id, user_id, body) SELECT p.id, $2::uuid, $3 FROM gallery_photos p
		WHERE p.id::text = $1 RETURNING id::text, (SELECT coalesce(created_by::text, '') FROM gallery_photos WHERE id = photo_id)`,
		photoID, userID, body).Scan(&id, &author)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if author != "" && author != userID {
		short := body
		if utf8.RuneCountInString(short) > galleryNotifyMax {
			short = string([]rune(short)[:galleryNotifyMax]) + "…"
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'gallery_comment', $3, $4)`,
			author, userID, short, "/company/photos/"+photoID+"#comments"); err != nil {
			return "", err
		}
	}
	return id, tx.Commit(ctx)
}

// PhotoComment by its id, with the photo's id.
func (s *Store) PhotoComment(ctx context.Context, id string) (PhotoComment, string, error) {
	var c PhotoComment
	var photo string
	err := s.db.QueryRow(ctx, `SELECT c.id::text, coalesce(c.user_id::text, ''), c.body, c.created_at, c.photo_id::text
		FROM gallery_comments c WHERE c.id::text = $1`, id).Scan(&c.ID, &c.UserID, &c.Body, &c.Created, &photo)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return c, photo, err
}

// DeletePhotoComment removes a comment (the caller checks: its author or a keeper).
func (s *Store) DeletePhotoComment(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM gallery_comments WHERE id::text = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		err = ErrNotFound
	}
	return err
}
