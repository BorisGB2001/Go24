-- The files of the «Файл» fields of business processes, as the parameters of that type: a file attached at
-- the start, in an answer to a request (or handed to a child process) belongs to the process — those who see the process
-- may download it. One file may be of several processes (a parent's handed to the processes it starts).
CREATE TABLE bp_files (
    instance_id uuid NOT NULL REFERENCES bp_instances(id) ON DELETE CASCADE,
    file_id     uuid NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    PRIMARY KEY (instance_id, file_id)
);
CREATE INDEX bp_files_file_idx ON bp_files (file_id);
