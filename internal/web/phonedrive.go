package web

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"go24/internal/drive"
	"go24/internal/storages"
	"go24/internal/telephony"
)

// ---------- «Записанные звонки»: the recorded conversations on the company's drive ----------

// phoneDriveLater puts the call's record on the company's drive after the call (Asterisk has closed the file by the
// time the call's log comes, see phoneCDR).
func (s *Server) phoneDriveLater(id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := s.phoneToDrive(ctx, id); err != nil {
		s.Log.Error("telephony record drive", "err", err, "call", id)
	}
}

// phoneToDrive copies the call's record to the company's drive: «Записанные звонки» / the month of the call, the file
// named by its time and the other end. The folder is the administrators' (its rights close it to the others, who get
// it from them); one already copied is not copied again.
func (s *Server) phoneToDrive(ctx context.Context, id string) error {
	if s.Drive == nil || s.Storages == nil || s.Telephony.Records == "" {
		return nil
	}
	c, err := s.Telephony.CallByID(ctx, id)
	if err != nil || c.Record == "" || c.DriveFile != "" {
		return err
	}
	f, err := os.Open(filepath.Join(s.Telephony.Records, filepath.Base(c.Record)))
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	d := drive.Drive{Kind: "company", CanWrite: true, CanManage: true}
	if err := s.Drive.Fits(ctx, d, st.Size()); err != nil {
		return err
	}
	number, _, emp := c.Outside()
	if emp == "" { // a call between employees
		number, emp = c.To, c.FromUser.UserID
	}
	if emp == "" {
		return nil
	}
	top, err := s.phoneDriveFolder(ctx, emp, d, "", s.portalTr("tel.record_folder"), true)
	if err != nil {
		return err
	}
	at := c.Started.Local()
	month, err := s.phoneDriveFolder(ctx, emp, d, top, at.Format("2006-01"), false)
	if err != nil {
		return err
	}
	name := at.Format("2006-01-02 15-04-05") + " " + strings.NewReplacer("/", "-", "\\", "-").Replace(number) + ".wav"
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), "call.wav")
	if err := s.Storages.Put(ctx, emp, false, "", key, f, st.Size(), "audio/wav"); err != nil {
		return err
	}
	fid, err := s.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: name, ContentType: "audio/wav", Size: st.Size(), UploadedBy: emp,
		Drive: d.Kind, FolderID: month})
	if err != nil {
		return err
	}
	return s.Telephony.SetDriveFile(ctx, c.ID, fid)
}

// phoneDriveFolder is the folder of that name in parent, made when there is none; a closed one is the administrators'
// only (given to them again when it has lost its rights).
func (s *Server) phoneDriveFolder(ctx context.Context, userID string, d drive.Drive, parent, name string, closed bool) (string, error) {
	folders, _, _, err := s.Drive.List(ctx, d, parent)
	if err != nil {
		return "", err
	}
	id := ""
	for _, f := range folders {
		if strings.EqualFold(f.Name, name) {
			id = f.ID
			break
		}
	}
	if id == "" {
		if id, err = s.Drive.CreateFolder(ctx, userID, d, parent, name); err != nil {
			return "", err
		}
	}
	if !closed {
		return id, nil
	}
	rights, err := s.Drive.FolderRights(ctx, []string{id})
	if err != nil || len(rights[id]) > 0 {
		return id, err
	}
	admins, err := s.admins(ctx)
	if err != nil {
		return "", err
	}
	for _, a := range admins {
		if err := s.Drive.SetRight(ctx, a.ID, d, id, "user", a.ID, "full"); err != nil {
			return "", err
		}
	}
	return id, nil
}

// phoneDriveOn tells whether the call's record is to go to the company's drive.
func (s *Server) phoneDriveOn(ctx context.Context, c telephony.CDR) bool {
	if c.Record == "" || s.Drive == nil || s.Storages == nil {
		return false
	}
	if st := c.Status(); st != telephony.StatusAnswered && st != telephony.StatusVoicemail {
		return false
	}
	on, err := s.Telephony.RecordDrive(ctx)
	if err != nil {
		s.Log.Error("telephony record drive", "err", err)
	}
	return on
}
