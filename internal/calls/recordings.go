package calls

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------- Recording of calls: LiveKit Egress records the room into a file ----------

// Egress is a recording as LiveKit Egress tells about it.
type Egress struct {
	ID, Status, Error string
	File              string // where Egress wrote the file (its own path), when it is over
	Size              int64
	Duration          time.Duration
}

// Over: the recording will not go on — its file is written (EGRESS_COMPLETE) or it failed.
func (e Egress) Over() bool {
	switch e.Status {
	case "EGRESS_COMPLETE", "EGRESS_FAILED", "EGRESS_ABORTED", "EGRESS_LIMIT_REACHED":
		return true
	}
	return false
}

// Done: the file is written whole (a recording stopped by its limit is whole too).
func (e Egress) Done() bool {
	return (e.Status == "EGRESS_COMPLETE" || e.Status == "EGRESS_LIMIT_REACHED") && e.File != ""
}

// egressInfo is LiveKit's EgressInfo (protojson: int64 comes as a string).
type egressInfo struct {
	EgressID    string `json:"egress_id"`
	Status      any    `json:"status"`
	Error       string `json:"error"`
	FileResults []struct {
		Filename string      `json:"filename"`
		Size     json.Number `json:"size"`
		Duration json.Number `json:"duration"` // nanoseconds
	} `json:"file_results"`
}

var egressStatuses = []string{"EGRESS_STARTING", "EGRESS_ACTIVE", "EGRESS_ENDING", "EGRESS_COMPLETE", "EGRESS_FAILED",
	"EGRESS_ABORTED", "EGRESS_LIMIT_REACHED"}

func (i egressInfo) egress() Egress {
	e := Egress{ID: i.EgressID, Error: i.Error}
	switch v := i.Status.(type) {
	case string:
		e.Status = v
	case float64: // the enum as a number
		if n := int(v); n >= 0 && n < len(egressStatuses) {
			e.Status = egressStatuses[n]
		}
	}
	if e.Status == "" {
		e.Status = egressStatuses[0]
	}
	if len(i.FileResults) > 0 {
		f := i.FileResults[0]
		e.File = f.Filename
		e.Size, _ = strconv.ParseInt(f.Size.String(), 10, 64)
		ns, _ := strconv.ParseInt(f.Duration.String(), 10, 64)
		e.Duration = time.Duration(ns)
	}
	return e
}

// StartRecording asks Egress to record the room as everyone sees it (the grid of video, or sound only) into a file
// under dir of Egress's own disk.
func (p *Provider) StartRecording(ctx context.Context, room, dir string, audio bool) (Egress, error) {
	name, kind, layout := room+"-{time}.mp4", "MP4", "grid"
	if audio {
		name, kind = room+"-{time}.ogg", "OGG"
	}
	in := map[string]any{"room_name": room, "layout": layout, "audio_only": audio,
		"file_outputs": []map[string]any{{"file_type": kind, "filepath": dir + "/" + name}}}
	var out egressInfo
	found, err := p.twirp(ctx, "Egress", "StartRoomCompositeEgress", map[string]any{"room": room, "roomRecord": true}, in, &out)
	if err != nil {
		return Egress{}, err
	}
	if !found {
		return Egress{}, ErrNoRoom
	}
	if out.EgressID == "" {
		return Egress{}, errors.New("livekit: no egress id")
	}
	return out.egress(), nil
}

// StopRecording stops the recording; Egress then writes the file to its end.
func (p *Provider) StopRecording(ctx context.Context, egressID string) error {
	_, err := p.twirp(ctx, "Egress", "StopEgress", map[string]any{"roomRecord": true}, map[string]string{"egress_id": egressID}, nil)
	return err
}

// Recording tells how the recording is; found false — Egress knows nothing of it.
func (p *Provider) Recording(ctx context.Context, egressID string) (Egress, bool, error) {
	var out struct {
		Items []egressInfo `json:"items"`
	}
	if _, err := p.twirp(ctx, "Egress", "ListEgress", map[string]any{"roomRecord": true}, map[string]string{"egress_id": egressID}, &out); err != nil {
		return Egress{}, false, err
	}
	for _, i := range out.Items {
		if i.EgressID == egressID {
			return i.egress(), true, nil
		}
	}
	return Egress{}, false, nil
}

// Recordings are the recordings of calls go24 started and keeps until their files are on the Drive.
type Recordings struct{ DB *pgxpool.Pool }

// Recording is one recording of a call.
type Recording struct {
	ID, Room, Title, Query, EgressID, StartedBy, State string
	Audio                                              bool
	Started                                            time.Time
	Stopped                                            *time.Time
}

// Recording states.
const (
	RecordingActive = "active"
	RecordingDone   = "done"
	RecordingFailed = "failed"
)

// ErrNoRoom: nobody is in the call yet, there is nothing to record.
var ErrNoRoom = errors.New("calls: the room is empty")

// ErrRecording: the room is being recorded already.
var ErrRecording = errors.New("calls: the call is being recorded already")

const (
	settingRecording  = "call_recording"
	settingTranscribe = "call_transcribe"
)

// Enabled: the administrators let calls be recorded.
func (rs *Recordings) Enabled(ctx context.Context) (bool, error) {
	return rs.setting(ctx, settingRecording)
}

// SetEnabled lets calls be recorded or not.
func (rs *Recordings) SetEnabled(ctx context.Context, userID string, on bool) error {
	return rs.setSetting(ctx, userID, settingRecording, on)
}

// Transcribing: the administrators let the recordings be turned into text.
func (rs *Recordings) Transcribing(ctx context.Context) (bool, error) {
	return rs.setting(ctx, settingTranscribe)
}

// SetTranscribing lets the recordings be turned into text or not.
func (rs *Recordings) SetTranscribing(ctx context.Context, userID string, on bool) error {
	return rs.setSetting(ctx, userID, settingTranscribe, on)
}

func (rs *Recordings) setting(ctx context.Context, key string) (bool, error) {
	var on bool
	err := rs.DB.QueryRow(ctx, `SELECT value = 'true'::jsonb FROM company_settings WHERE key = $1`, key).Scan(&on)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return on, err
}

func (rs *Recordings) setSetting(ctx context.Context, userID, key string, on bool) error {
	_, err := rs.DB.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, to_jsonb($2::boolean), $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		key, on, userID)
	return err
}

const recordingCols = `id::text, lk_room, title, call_query, egress_id, started_by::text, state, audio, started_at, stopped_at`

func scanRecording(row pgx.Row) (Recording, error) {
	var r Recording
	err := row.Scan(&r.ID, &r.Room, &r.Title, &r.Query, &r.EgressID, &r.StartedBy, &r.State, &r.Audio, &r.Started, &r.Stopped)
	return r, err
}

// Add remembers a recording Egress has started; ErrRecording — another one goes on in the room.
func (rs *Recordings) Add(ctx context.Context, r Recording) (Recording, error) {
	out, err := scanRecording(rs.DB.QueryRow(ctx, `INSERT INTO call_recordings (lk_room, title, call_query, egress_id, audio, started_by)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+recordingCols, r.Room, r.Title, r.Query, r.EgressID, r.Audio, r.StartedBy))
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" && pe.ConstraintName == "call_recordings_active" {
		return out, ErrRecording
	}
	return out, err
}

// Current is the recording going on in the room; found false — none.
func (rs *Recordings) Current(ctx context.Context, room string) (Recording, bool, error) {
	r, err := scanRecording(rs.DB.QueryRow(ctx, `SELECT `+recordingCols+` FROM call_recordings
		WHERE lk_room = $1 AND state = 'active' AND stopped_at IS NULL`, room))
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// Stop marks the recording stopped (once): false — it was stopped already.
func (rs *Recordings) Stop(ctx context.Context, id, userID string) (bool, error) {
	var by *string
	if userID != "" {
		by = &userID
	}
	tag, err := rs.DB.Exec(ctx, `UPDATE call_recordings SET stopped_at = now(), stopped_by = $2
		WHERE id::text = $1 AND state = 'active' AND stopped_at IS NULL`, id, by)
	return tag.RowsAffected() == 1, err
}

// Going are the recordings whose files are not saved yet, the oldest first.
func (rs *Recordings) Going(ctx context.Context) ([]Recording, error) {
	rows, err := rs.DB.Query(ctx, `SELECT `+recordingCols+` FROM call_recordings WHERE state = 'active' ORDER BY started_at`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recording, error) { return scanRecording(row) })
}

// Finish ends the recording: its file on the Drive (fileID), or what went wrong (failure); transcribe — its text is
// to be made (NextTranscript).
func (rs *Recordings) Finish(ctx context.Context, id, fileID, failure string, transcribe bool) error {
	state, file, transcript := RecordingDone, &fileID, ""
	if fileID == "" {
		state, file = RecordingFailed, nil
	} else if transcribe {
		transcript = "pending"
	}
	_, err := rs.DB.Exec(ctx, `UPDATE call_recordings SET state = $2, file_id = $3::uuid, error = $4, ended_at = now(),
		stopped_at = coalesce(stopped_at, now()), transcript = $5 WHERE id::text = $1 AND state = 'active'`, id, state, file, failure, transcript)
	return err
}

// TranscriptTries is how many times go24-worker begins the text of a recording before giving it up (it may fall
// over in the middle of a long one).
const TranscriptTries = 3

// NextTranscript is the oldest saved recording whose text is to be made, with its file on the Drive and which try
// this is; found false — none waits. A recording tried TranscriptTries times already (go24-worker fell over in the
// middle) fails.
func (rs *Recordings) NextTranscript(ctx context.Context) (rec Recording, fileID string, try int, found bool, err error) {
	if _, err := rs.DB.Exec(ctx, `UPDATE call_recordings SET transcript = 'failed'
		WHERE transcript = 'pending' AND (transcript_tries >= $1 OR file_id IS NULL)`, TranscriptTries); err != nil {
		return rec, "", 0, false, err
	}
	var r Recording
	err = rs.DB.QueryRow(ctx, `UPDATE call_recordings SET transcript_tries = transcript_tries + 1
		WHERE id = (SELECT id FROM call_recordings WHERE transcript = 'pending' ORDER BY ended_at LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING `+recordingCols+`, file_id::text, transcript_tries`).
		Scan(&r.ID, &r.Room, &r.Title, &r.Query, &r.EgressID, &r.StartedBy, &r.State, &r.Audio, &r.Started, &r.Stopped, &fileID, &try)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, "", 0, false, nil
	}
	return r, fileID, try, err == nil, err
}

// Transcribed ends the text of the recording: its file on the Drive, or "" — it could not be made.
func (rs *Recordings) Transcribed(ctx context.Context, id, fileID string) error {
	state, file := "done", &fileID
	if fileID == "" {
		state, file = "failed", nil
	}
	_, err := rs.DB.Exec(ctx, `UPDATE call_recordings SET transcript = $2, transcript_file = $3::uuid
		WHERE id::text = $1 AND transcript = 'pending'`, id, state, file)
	return err
}
