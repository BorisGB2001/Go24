package web

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/docgen"
	"go24/internal/sign"
	"go24/internal/storages"
)

// «Подпись»: blocks on the pages of the document — the sender puts the signers' signatures, names, signing
// dates, the company's stamp and texts on the pages (/sign/{id}/blocks/edit); the signed PDF has them in their places.

// signBlockJSON is a block as the editor and REST have it: the signer — by their number in the document (ord).
type signBlockJSON struct {
	ID     string  `json:"id,omitempty"` // given out: a field is filled in by it
	Signer int     `json:"signer"`
	Kind   string  `json:"kind"` // signature | name | date | stamp | text | field
	Page   int     `json:"page"`
	X      float64 `json:"x"` // the box, in fractions of the page's size from its top left corner
	Y      float64 `json:"y"`
	W      float64 `json:"w"`
	H      float64 `json:"h"`
	Text   string  `json:"text,omitempty"` // a text; the caption of a field
	// Required: a field the signer cannot leave empty; Value — what they filled in (given out)
	Required bool   `json:"required,omitempty"`
	Value    string `json:"value,omitempty"`
}

func signBlocksJSON(d sign.Doc, blocks []sign.Block) []signBlockJSON {
	out := []signBlockJSON{}
	for _, b := range blocks {
		if p := d.Party(b.PartyID); p != nil {
			out = append(out, signBlockJSON{b.ID, p.Ord, b.Kind, b.Page, b.X, b.Y, b.W, b.H, b.Text, b.Required, b.Value})
		}
	}
	return out
}

// signBlocksOf are the blocks given, of the document's signers by their numbers (an unknown one — none: refused).
func signBlocksOf(d sign.Doc, in []signBlockJSON) ([]sign.Block, error) {
	out := make([]sign.Block, 0, len(in))
	for _, b := range in {
		id := ""
		for _, p := range d.Parties {
			if p.Ord == b.Signer {
				id = p.ID
			}
		}
		if id == "" {
			return nil, sign.ErrBlocks
		}
		out = append(out, sign.Block{PartyID: id, Kind: b.Kind, Page: b.Page, X: b.X, Y: b.Y, W: b.W, H: b.H, Text: b.Text,
			Required: b.Required})
	}
	return out, nil
}

// signBlocksEditable: the blocks can still be put — the document is being signed and none has signed it.
func signBlocksEditable(d sign.Doc) bool {
	if d.Status != "signing" {
		return false
	}
	for _, p := range d.Parties {
		if p.Status == "signed" && !p.Reviewer() {
			return false
		}
	}
	return true
}

// signPDF is the document in PDF (see signPDFID).
func (s *Server) signPDF(r *http.Request, d sign.Doc) ([]byte, error) {
	id, err := s.signPDFID(r, d)
	var m storages.FileMeta
	if err == nil {
		m, err = s.Storages.FileMeta(r.Context(), id)
	}
	if err != nil {
		return nil, err
	}
	return s.readFile(r, m, signMax)
}

// signBlocksPage is the editor of the blocks: the pages of the document, the signers and the blocks to put (the
// sender, an administrator).
func (s *Server) signBlocksPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	back := "/sign/" + r.PathValue("id")
	if err == nil && !s.signEdits(r.Context(), sess, d) {
		err = sign.ErrNotFound
	}
	if err == nil && !signBlocksEditable(d) {
		err = sign.ErrBlocksSigned
	}
	var pages []docgen.PageSize
	if err == nil {
		var pdf []byte
		if pdf, err = s.signPDF(r, d); err == nil {
			pages, err = docgen.PDFPages(r.Context(), pdf)
		}
	}
	var blocks []sign.Block
	if err == nil {
		blocks, err = s.Sign.Blocks(r.Context(), d.ID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	images, _ := s.CRM.DocImages(r.Context())
	raw, _ := json.Marshal(signBlocksJSON(d, blocks))
	s.render(w, r, "signblocks", page{Title: d.Title, Nav: "sign", Data: map[string]any{"Doc": d, "Pages": pages,
		"Blocks": string(raw), "Kinds": sign.BlockKinds, "Stamp": images["Stamp"] != ""}})
}

// signBlocksSave keeps the blocks of the editor (the form's «blocks» — JSON of signBlockJSON).
func (s *Server) signBlocksSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	back := "/sign/" + r.PathValue("id")
	if err == nil && !s.signEdits(r.Context(), sess, d) {
		err = sign.ErrNotFound
	}
	var in []signBlockJSON
	if err == nil && json.Unmarshal([]byte(r.FormValue("blocks")), &in) != nil {
		err = sign.ErrBlocks
	}
	var blocks []sign.Block
	if err == nil {
		blocks, err = signBlocksOf(d, in)
	}
	if err == nil {
		err = s.Sign.SetBlocks(r.Context(), d.ID, blocks)
	}
	if err != nil {
		s.taskErr(w, r, err, back+"/blocks/edit")
		return
	}
	s.Log.Info("sign blocks", "user_id", sess.UserID, "doc", d.ID, "blocks", len(blocks))
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "sign.blocks.saved", len(blocks))), http.StatusSeeOther)
}

// signPageImage is a page of the document as a picture, for the editor of the blocks (those who see it).
func (s *Server) signPageImage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	d, err := s.signOf(r, sess)
	n, _ := strconv.Atoi(r.PathValue("n"))
	var pic []byte
	if err == nil && n >= 1 {
		var pdf []byte
		if pdf, err = s.signPDF(r, d); err == nil {
			pic, err = docgen.PDFPagePNG(r.Context(), pdf, n, 900)
		}
	}
	if err != nil || pic == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=600")
	_, _ = w.Write(pic)
}

// signStamp puts the blocks of the document on the pages of its PDF, those of the signers who signed; false — it has
// none or they could not be put (the PDF stays as it is).
func (s *Server) signStamp(r *http.Request, d sign.Doc, pdf []byte) ([]byte, bool) {
	blocks, err := s.Sign.Blocks(r.Context(), d.ID)
	if err != nil || len(blocks) == 0 {
		return nil, false
	}
	var stamp []byte // the company's stamp of «Моя компания»
	if images, err := s.CRM.DocImages(r.Context()); err == nil && images["Stamp"] != "" {
		if m, err := s.Storages.FileMeta(r.Context(), images["Stamp"]); err == nil {
			stamp, _ = s.readFile(r, m, 5<<20)
		}
	}
	sigs := map[string][]byte{}
	var stamps []docgen.Stamp
	for _, b := range blocks {
		p := d.Party(b.PartyID)
		if p == nil || p.Status != "signed" {
			continue
		}
		// a text is drawn about three pixels a point of an A4 page high
		text := func(t string) []byte {
			pic, _ := docgen.TextPNG(t, min(max(int(b.H*842*3), 24), 240))
			return pic
		}
		var pic []byte
		switch b.Kind {
		case "signature":
			if _, ok := sigs[p.ID]; !ok && p.HasSignature {
				sigs[p.ID], _ = s.Sign.PartySignature(r.Context(), p.ID)
			}
			if pic = sigs[p.ID]; pic == nil {
				pic = text(p.Name)
			}
		case "name":
			pic = text(p.Name)
		case "date":
			if p.SignedAt != nil {
				pic = text(p.SignedAt.In(time.Local).Format("02.01.2006"))
			}
		case "stamp":
			pic = stamp
		case "text":
			pic = text(b.Text)
		case "field":
			if b.Value != "" {
				pic = text(b.Value)
			}
		}
		if pic != nil {
			stamps = append(stamps, docgen.Stamp{Page: b.Page, X: b.X, Y: b.Y, W: b.W, H: b.H, Image: pic})
		}
	}
	out, err := docgen.StampPDF(r.Context(), pdf, stamps)
	if err != nil {
		s.Log.Warn("sign blocks", "doc", d.ID, "err", err)
		return nil, false
	}
	return out, len(stamps) > 0
}

// signFields keeps what the signer filled in their fields before they sign (values by the blocks' ids), when it is
// their turn; not for one who approves.
func (s *Server) signFields(r *http.Request, d sign.Doc, p *sign.Party, values map[string]string) error {
	if p == nil || p.Reviewer() {
		return nil
	}
	if cur := d.Current(); d.Status != "signing" || cur == nil || cur.ID != p.ID {
		return sign.ErrTurn
	}
	return s.Sign.FillFields(r.Context(), d.ID, p.ID, values)
}

// signFieldsForm are the values of the fields from the form: «field_<block id>».
func signFieldsForm(r *http.Request) map[string]string {
	_ = r.ParseForm()
	out := map[string]string{}
	for k, v := range r.PostForm {
		if id, ok := strings.CutPrefix(k, "field_"); ok && len(v) > 0 {
			out[id] = v[0]
		}
	}
	return out
}

// signMyFields are the fields the signer whose turn it is fills in (none — not their turn).
func (s *Server) signMyFields(r *http.Request, d sign.Doc, p *sign.Party) []sign.Block {
	if p == nil || p.Reviewer() || d.Status != "signing" || p.Status != "ready" {
		return nil
	}
	blocks, err := s.Sign.Blocks(r.Context(), d.ID)
	if err != nil {
		s.Log.Warn("sign fields", "doc", d.ID, "err", err)
	}
	return sign.Fields(blocks, p.ID)
}

// apiSignBlocks gives the blocks of the document: [{"signer": 1, "kind": "signature", "page": 1, "x": 0.1, …}].
func (s *Server) apiSignBlocks(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, _, err := s.apiSignDocOf(r, p)
	var blocks []sign.Block
	if err == nil {
		blocks, err = s.Sign.Blocks(r.Context(), d.ID)
	}
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"blocks": signBlocksJSON(d, blocks), "editable": signBlocksEditable(d)})
	return nil
}

// apiSignBlocksSet puts the blocks on the document instead of those it had (the sender, while none has signed):
// JSON {"blocks": [{"signer": 1, "kind": "date", "page": 2, "x": …, "y": …, "w": …, "h": …}, …]}.
func (s *Server) apiSignBlocksSet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	d, sess, err := s.apiSignDocOf(r, p)
	if err == nil && !s.signEdits(r.Context(), sess, d) {
		err = sign.ErrNotFound
	}
	var in struct {
		Blocks []signBlockJSON `json:"blocks"`
	}
	if err == nil {
		err = readJSON(r, &in)
	}
	var blocks []sign.Block
	if err == nil {
		blocks, err = signBlocksOf(d, in.Blocks)
	}
	if err == nil {
		err = s.Sign.SetBlocks(r.Context(), d.ID, blocks)
	}
	if err != nil {
		return err
	}
	s.Log.Info("sign blocks", "user_id", p.UserID, "doc", d.ID, "blocks", len(blocks), "api", true)
	return s.apiSignBlocks(w, r, p)
}
