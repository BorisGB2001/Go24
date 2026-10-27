-- «Отказаться от доступа»: the employee gave up a folder or a file of someone's «Мой диск» shared with
-- them; a share to their department no longer reaches them, until the owner opens it to them in person again.
CREATE TABLE drive_share_declines (
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    folder_id  uuid REFERENCES drive_folders(id) ON DELETE CASCADE,
    file_id    uuid REFERENCES files(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((folder_id IS NULL) <> (file_id IS NULL))
);
CREATE UNIQUE INDEX drive_share_declines_folder_idx ON drive_share_declines (user_id, folder_id) WHERE folder_id IS NOT NULL;
CREATE UNIQUE INDEX drive_share_declines_file_idx ON drive_share_declines (user_id, file_id) WHERE file_id IS NOT NULL;
