package web

import (
	"cmp"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/qes"
	"go24/internal/sign"
)

// «Подпись» with КЭП: the signer signs the document's PDF with their qualified certificate through
// the CryptoPro browser plug-in (js/qes.js); the detached CAdES signature is checked and kept, and the signing sheet
// says whose certificate it is.

// signQESMax is the largest PDF signed with КЭП.
const signQESMax = 64 << 20

// signQES checks the signer's КЭП of the document's PDF (cms — the signature as the plug-in gives it, base64) and
// keeps it; signing then needs no code.
func (s *Server) signQES(r *http.Request, d sign.Doc, p *sign.Party, cms []byte) error {
	if len(strings.TrimSpace(string(cms))) == 0 {
		return sign.ErrQES
	}
	pdf, err := s.signPDFBytes(r, d)
	if err != nil {
		return err
	}
	info, err := qes.Verify(qes.Decode(cms), pdf, time.Now())
	if err != nil {
		s.Log.Info("sign qes", "doc", d.ID, "party", p.ID, "err", err)
		return err
	}
	s.Log.Info("sign qes", "doc", d.ID, "party", p.ID, "serial", info.Serial, "alg", info.Alg)
	return s.Sign.SetQES(r.Context(), d.ID, p.ID, qes.Decode(cms), info)
}

// signPDFBytes is the document's PDF, the one signed with КЭП.
func (s *Server) signPDFBytes(r *http.Request, d sign.Doc) ([]byte, error) {
	id, err := s.signPDFID(r, d)
	if err != nil {
		return nil, err
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		return nil, err
	}
	return s.readFile(r, m, signQESMax)
}

// signQESLine is how a signer with КЭП signed, for the signing sheet: the certificate's owner, number, issuer and term.
func (s *Server) signQESLine(r *http.Request, c *qes.Signed) string {
	owner := c.Owner
	if who := strings.TrimSpace(c.Surname + " " + c.Given); who != "" && who != owner {
		owner += ", " + who
	}
	var ids []string
	for _, v := range [][2]string{{"sign.qes.inn", cmp.Or(c.INNLE, c.INN)}, {"sign.qes.ogrn", c.OGRN}, {"sign.qes.snils", c.SNILS}} {
		if v[1] != "" {
			ids = append(ids, s.tr(r, v[0], v[1]))
		}
	}
	if len(ids) > 0 {
		owner += " (" + strings.Join(ids, ", ") + ")"
	}
	return s.tr(r, "sign.how.qes", owner, c.Serial, c.Issuer, c.NotBefore.In(time.Local).Format("02.01.2006"), c.NotAfter.In(time.Local).Format("02.01.2006"))
}

// signQESFile gives a signer's КЭП: the detached signature (.sig) of the document's PDF.
func (s *Server) signQESFile(w http.ResponseWriter, r *http.Request, d sign.Doc, err error) {
	p := d.Party(r.PathValue("party"))
	var sig []byte
	if err == nil && p != nil {
		sig, err = s.Sign.QESSig(r.Context(), d.ID, p.ID)
	}
	if err != nil || p == nil {
		http.NotFound(w, r)
		return
	}
	name := strings.Map(func(c rune) rune {
		if strings.ContainsRune(`/\:*?"<>|`, c) {
			return '_'
		}
		return c
	}, d.Title+" — "+p.Name)
	w.Header().Set("Content-Type", "application/pkcs7-signature")
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name+".pdf.sig"))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(sig)
}

func (s *Server) signQESGet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	s.signQESFile(w, r, d, err)
}
