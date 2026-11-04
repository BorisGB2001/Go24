package web

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"go24/internal/apikeys"
	"go24/internal/i18n"
	"go24/internal/sign"
)

// Checking a signed document: the signing sheet has the «Код проверки» and a QR code of the public page
// /sg/check/{code}; there, or by uploading the file itself, anyone learns whether the document was signed in go24,
// by whom and when. Neither e-mails nor IP addresses are shown.

// signCheckURL is the public page checking the document.
func (s *Server) signCheckURL(d sign.Doc) string {
	return s.BaseURL + "/sg/check/" + sign.ShowCode(d.VerifyCode)
}

// signCheckSigner is a signer as the check shows them.
type signCheckSigner struct {
	Name     string     `json:"name"`
	Side     string     `json:"side"`   // company | client | employee
	Role     string     `json:"role"`   // signer | reviewer
	Status   string     `json:"status"` // wait | ready | signed | refused
	SignedAt *time.Time `json:"signed_at,omitempty"`
}

// signCheck is what the check tells of a document.
type signCheck struct {
	Found     bool              `json:"found"`
	Match     string            `json:"match,omitempty"` // by the file: original (as sent for signing) | signed (the signed copy)
	Code      string            `json:"verify_code,omitempty"`
	Title     string            `json:"title,omitempty"`
	Status    string            `json:"status,omitempty"` // signing | done | refused | cancelled
	Author    string            `json:"author,omitempty"`
	CreatedAt *time.Time        `json:"created_at,omitempty"`
	DoneAt    *time.Time        `json:"done_at,omitempty"`
	Hash      string            `json:"sha256,omitempty"`
	Result    string            `json:"result_sha256,omitempty"`
	Signers   []signCheckSigner `json:"signers,omitempty"`
}

func signCheckOf(d sign.Doc) signCheck {
	c := signCheck{Found: true, Code: sign.ShowCode(d.VerifyCode), Title: d.Title, Status: d.Status, Author: d.Author,
		CreatedAt: &d.CreatedAt, DoneAt: d.DoneAt, Hash: d.Hash, Result: d.ResultHash, Signers: []signCheckSigner{}}
	for _, p := range d.Parties {
		side, role := "company", "signer"
		switch {
		case p.Side == "employee":
			side = "employee"
		case !p.Employee():
			side = "client"
		}
		if p.Reviewer() {
			role = "reviewer"
		}
		c.Signers = append(c.Signers, signCheckSigner{Name: p.Name, Side: side, Role: role, Status: p.Status, SignedAt: p.SignedAt})
	}
	return c
}

// signCheckFind checks by the verify code or by the file's SHA-256; err — not a not-found.
func (s *Server) signCheckFind(r *http.Request, code, hash string) (signCheck, error) {
	var d sign.Doc
	var err error
	signed := false
	if code != "" {
		d, err = s.Sign.ByCode(r.Context(), code)
	} else {
		d, signed, err = s.Sign.ByHash(r.Context(), hash)
	}
	if errors.Is(err, sign.ErrNotFound) {
		return signCheck{}, nil
	}
	if err != nil {
		return signCheck{}, err
	}
	c := signCheckOf(d)
	if hash != "" {
		c.Match = "original"
		if signed {
			c.Match = "signed"
		}
	}
	return c, nil
}

// signCheckPage: GET /sg/check — the form (a code or a file), GET /sg/check/{code} — the document of the code.
func (s *Server) signCheckPage(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("code")
	if code == "" && r.URL.Query().Get("code") != "" {
		http.Redirect(w, r, "/sg/check/"+url.PathEscape(sign.ShowCode(sign.NormCode(r.URL.Query().Get("code")))), http.StatusSeeOther)
		return
	}
	data := map[string]any{"Code": code}
	if code != "" {
		c, err := s.signCheckFind(r, code, "")
		if err != nil {
			s.Log.Error("sign check", "err", err)
			http.Error(w, s.tr(r, "err.internal"), http.StatusInternalServerError)
			return
		}
		data["Asked"], data["Check"] = true, c
		if !c.Found {
			w.WriteHeader(http.StatusNotFound)
		}
	}
	s.signCheckRender(w, r, data)
}

// signCheckFile: POST /sg/check — the uploaded file is checked by its SHA-256; it is not kept.
func (s *Server) signCheckFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, signMax+1<<20)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.signCheckRender(w, r, map[string]any{"Msg": s.tr(r, "sign.check.no_file")})
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		s.signCheckRender(w, r, map[string]any{"Msg": s.tr(r, "sign.check.no_file")})
		return
	}
	c, err := s.signCheckFind(r, "", sign.Hash(data))
	if err != nil {
		s.Log.Error("sign check", "err", err)
		http.Error(w, s.tr(r, "err.internal"), http.StatusInternalServerError)
		return
	}
	s.signCheckRender(w, r, map[string]any{"Asked": true, "Check": c, "File": hdr.Filename, "FileHash": sign.Hash(data)})
}

func (s *Server) signCheckRender(w http.ResponseWriter, r *http.Request, data map[string]any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.render(w, r, "signcheck", page{TitleKey: "sign.check.title", Data: data})
}

// apiSignCheck: GET /api/v1/sign/check?code= or ?sha256= — the public check of a document (found: false — none).
func (s *Server) apiSignCheck(w http.ResponseWriter, r *http.Request, _ apikeys.Principal) error {
	q := r.URL.Query()
	code, hash := q.Get("code"), q.Get("sha256")
	if code == "" && hash == "" {
		return i18n.E("sign.check.need_code")
	}
	c, err := s.signCheckFind(r, code, hash)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, c)
	return nil
}
