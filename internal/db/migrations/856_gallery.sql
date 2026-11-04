-- «Фотогалерея» of the company (GO24DEV-929): albums (nested), who sees them, who adds photos;
-- the photos with their captions, «Нравится» and comments.
CREATE TABLE gallery_albums (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    parent_id   uuid REFERENCES gallery_albums(id) ON DELETE CASCADE,
    title       text NOT NULL,
    description text NOT NULL DEFAULT '',
    cover_id    uuid,
    anyone_adds boolean NOT NULL DEFAULT false, -- whoever sees the album adds photos, not only its author and keepers
    created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gallery_albums_parent ON gallery_albums (parent_id);

-- «Кто видит»: people and departments (with sub-departments); none — everybody.
CREATE TABLE gallery_access (
    album_id      uuid NOT NULL REFERENCES gallery_albums(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    CHECK ((user_id IS NULL) <> (department_id IS NULL))
);
CREATE UNIQUE INDEX gallery_access_user ON gallery_access (album_id, user_id) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX gallery_access_dept ON gallery_access (album_id, department_id) WHERE department_id IS NOT NULL;

CREATE TABLE gallery_photos (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    album_id   uuid NOT NULL REFERENCES gallery_albums(id) ON DELETE CASCADE,
    file_key   text NOT NULL,
    thumb_key  text NOT NULL DEFAULT '',
    file_name  text NOT NULL,
    mime       text NOT NULL,
    size       bigint NOT NULL DEFAULT 0,
    width      int NOT NULL DEFAULT 0,
    height     int NOT NULL DEFAULT 0,
    caption    text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gallery_photos_album ON gallery_photos (album_id, created_at, id);

ALTER TABLE gallery_albums ADD FOREIGN KEY (cover_id) REFERENCES gallery_photos(id) ON DELETE SET NULL;

CREATE TABLE gallery_likes (
    photo_id   uuid NOT NULL REFERENCES gallery_photos(id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (photo_id, user_id)
);

CREATE TABLE gallery_comments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    photo_id   uuid NOT NULL REFERENCES gallery_photos(id) ON DELETE CASCADE,
    user_id    uuid REFERENCES users(id) ON DELETE SET NULL,
    body       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX gallery_comments_photo ON gallery_comments (photo_id, created_at);
