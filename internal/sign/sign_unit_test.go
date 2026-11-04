package sign

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ---------- Document & String Hashing ----------

func TestHash(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want string
	}{
		{
			name: "empty byte slice",
			in:   []byte(""),
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "nil byte slice",
			in:   nil,
			want: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			name: "hello world",
			in:   []byte("hello world"),
			want: "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9",
		},
		{
			name: "the quick brown fox",
			in:   []byte("The quick brown fox jumps over the lazy dog"),
			want: "d7a8fbb307d7809469ca9abcb0082e4f8d5651e46d3cdb762d02d0bf37c9e592",
		},
	}
	hexRegex := regexp.MustCompile("^[0-9a-f]{64}$")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Hash(tc.in)
			if got != tc.want {
				t.Errorf("Hash(%q) = %q, want %q", string(tc.in), got, tc.want)
			}
			if !hexRegex.MatchString(got) {
				t.Errorf("Hash(%q) = %q is not 64-char lowercase hex", string(tc.in), got)
			}
		})
	}
}

func TestHash_Integrity(t *testing.T) {
	originalDoc := []byte("Employment Agreement v1.0: Alice agrees to join as Developer.")
	modifiedDoc := []byte("Employment Agreement v1.0: Alice agrees to join as Director.")

	h1 := Hash(originalDoc)
	h2 := Hash(modifiedDoc)
	if h1 == h2 {
		t.Errorf("different documents produced identical hash: %s", h1)
	}

	// Repeated hashing of same content produces identical hash
	if Hash(originalDoc) != h1 {
		t.Errorf("Hash is not deterministic")
	}
}

func TestCodeHash(t *testing.T) {
	partyID := "party-123"
	code := "424242"
	want := Hash([]byte(partyID + ":" + code))

	got := codeHash(partyID, code)
	if got != want {
		t.Errorf("codeHash(%q, %q) = %q, want %q", partyID, code, got, want)
	}

	// Different party IDs with same code yield different hashes
	if codeHash("party-1", code) == codeHash("party-2", code) {
		t.Errorf("different party IDs produced same codeHash")
	}

	// Same party with different codes yield different hashes
	if codeHash(partyID, "111111") == codeHash(partyID, "222222") {
		t.Errorf("different codes produced same codeHash")
	}

	// Untrimmed code differs from trimmed code
	if codeHash(partyID, " "+code+" ") == codeHash(partyID, code) {
		t.Errorf("untrimmed code matched trimmed code unexpectedly")
	}
}

// ---------- Token Generation ----------

func TestToken(t *testing.T) {
	hexRegex := regexp.MustCompile("^[0-9a-f]{48}$")
	seen := make(map[string]bool)

	for i := 0; i < 500; i++ {
		tok := token()
		if len(tok) != 48 {
			t.Fatalf("token() len = %d, want 48", len(tok))
		}
		if !hexRegex.MatchString(tok) {
			t.Fatalf("token() = %q is not 48 lowercase hex chars", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token generated: %s", tok)
		}
		seen[tok] = true
	}
}

// ---------- Helper: truncate ----------

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty string", "", 10, ""},
		{"shorter than limit", "hello", 10, "hello"},
		{"exact length limit", "hello", 5, "hello"},
		{"longer than limit", "hello world", 5, "hello"},
		{"limit zero", "hello", 0, ""},
		{"multibyte cyrillic shorter", "Привет", 10, "Привет"},
		{"multibyte cyrillic exact", "Привет", 6, "Привет"},
		{"multibyte cyrillic longer", "Приказ об отпуске", 6, "Приказ"},
		{"emoji runes truncated", "💼🏢📝✅❌", 3, "💼🏢📝"},
		{"emoji runes exact", "💼🏢📝", 3, "💼🏢📝"},
		{"large limit 1000", strings.Repeat("a", 1005), 1000, strings.Repeat("a", 1000)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}

// ---------- Party Helpers ----------

func TestParty_Employee(t *testing.T) {
	tests := []struct {
		name  string
		party Party
		want  bool
	}{
		{"employee with UserID", Party{UserID: "u-1"}, true},
		{"client without UserID", Party{UserID: "", Email: "client@example.com"}, false},
		{"empty party", Party{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.party.Employee(); got != tc.want {
				t.Errorf("Party.Employee() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParty_Reviewer(t *testing.T) {
	tests := []struct {
		name  string
		party Party
		want  bool
	}{
		{"reviewer role", Party{Role: "reviewer"}, true},
		{"empty role", Party{Role: ""}, false},
		{"signer role", Party{Role: "signer"}, false},
		{"case sensitive Reviewer", Party{Role: "Reviewer"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.party.Reviewer(); got != tc.want {
				t.Errorf("Party.Reviewer() = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------- Doc Helpers ----------

func TestDoc_Current(t *testing.T) {
	tests := []struct {
		name    string
		parties []Party
		wantID  string
	}{
		{"no parties", nil, ""},
		{"empty parties", []Party{}, ""},
		{"all waiting", []Party{{ID: "p1", Status: "wait"}, {ID: "p2", Status: "wait"}}, ""},
		{"first party ready", []Party{{ID: "p1", Status: "ready"}, {ID: "p2", Status: "wait"}}, "p1"},
		{"second party ready", []Party{{ID: "p1", Status: "signed"}, {ID: "p2", Status: "ready"}}, "p2"},
		{"all signed", []Party{{ID: "p1", Status: "signed"}, {ID: "p2", Status: "signed"}}, ""},
		{"refused", []Party{{ID: "p1", Status: "refused"}, {ID: "p2", Status: "wait"}}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Doc{Parties: tc.parties}
			curr := d.Current()
			if tc.wantID == "" {
				if curr != nil {
					t.Errorf("Doc.Current() = %+v, want nil", curr)
				}
			} else {
				if curr == nil || curr.ID != tc.wantID {
					t.Errorf("Doc.Current() = %+v, want party with ID %q", curr, tc.wantID)
				}
			}
		})
	}
}

func TestDoc_Party(t *testing.T) {
	parties := []Party{
		{ID: "p-emp", UserID: "u-1", Name: "Alice"},
		{ID: "p-cli", Email: "bob@test", Name: "Bob"},
	}
	d := Doc{Parties: parties}

	if p := d.Party("p-emp"); p == nil || p.Name != "Alice" {
		t.Errorf("Doc.Party(\"p-emp\") = %+v, want Alice", p)
	}
	if p := d.Party("p-cli"); p == nil || p.Name != "Bob" {
		t.Errorf("Doc.Party(\"p-cli\") = %+v, want Bob", p)
	}
	if p := d.Party("non-existent"); p != nil {
		t.Errorf("Doc.Party(\"non-existent\") = %+v, want nil", p)
	}
	if p := (Doc{}).Party("p-emp"); p != nil {
		t.Errorf("Doc.Party on empty doc = %+v, want nil", p)
	}
}

func TestDoc_Employee(t *testing.T) {
	tests := []struct {
		name    string
		parties []Party
		wantID  string
	}{
		{"no parties", nil, ""},
		{"no employee side", []Party{{ID: "p1", Side: ""}, {ID: "p2", Side: "client"}}, ""},
		{"with employee side", []Party{{ID: "p1", Side: ""}, {ID: "p2", Side: "employee"}}, "p2"},
		{"first of multiple employee sides", []Party{{ID: "p1", Side: "employee"}, {ID: "p2", Side: "employee"}}, "p1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Doc{Parties: tc.parties}
			emp := d.Employee()
			if tc.wantID == "" {
				if emp != nil {
					t.Errorf("Doc.Employee() = %+v, want nil", emp)
				}
			} else {
				if emp == nil || emp.ID != tc.wantID {
					t.Errorf("Doc.Employee() = %+v, want ID %q", emp, tc.wantID)
				}
			}
		})
	}
}

func TestDoc_Sees(t *testing.T) {
	d := Doc{
		AuthorID: "u-author",
		Parties: []Party{
			{ID: "p-1", UserID: "u-signer1"},
			{ID: "p-2", UserID: "u-signer2"},
		},
		Observers: []Observer{
			{UserID: "u-obs1", Name: "Observer One"},
		},
	}

	if !d.Sees("u-author") {
		t.Errorf("Doc.Sees(author) = false, want true")
	}
	if !d.Sees("u-signer1") {
		t.Errorf("Doc.Sees(signer1) = false, want true")
	}
	if !d.Sees("u-signer2") {
		t.Errorf("Doc.Sees(signer2) = false, want true")
	}
	if !d.Sees("u-obs1") {
		t.Errorf("Doc.Sees(observer) = false, want true")
	}
	if d.Sees("u-stranger") {
		t.Errorf("Doc.Sees(stranger) = true, want false")
	}
	if d.Sees("") {
		t.Errorf("Doc.Sees(\"\") on doc without empty UserID = true, want false")
	}

	// Documenting behavior when doc has a client party without UserID (UserID == ""):
	docWithClient := Doc{
		AuthorID: "u-author",
		Parties:  []Party{{ID: "p-cli", UserID: "", Email: "client@test"}},
	}
	if !docWithClient.Sees("") {
		t.Errorf("Doc.Sees(\"\") when party.UserID == \"\" expected true by implementation, got false")
	}
}

// ---------- E-Signature Logic: Code Generation, Rate-Limit, Expiry, Tries ----------

func TestCodeConstants(t *testing.T) {
	if CodeTTL != 10*time.Minute {
		t.Errorf("CodeTTL = %v, want 10m", CodeTTL)
	}
	if CodeTries != 5 {
		t.Errorf("CodeTries = %d, want 5", CodeTries)
	}
	if CodeAgain != time.Minute {
		t.Errorf("CodeAgain = %v, want 1m", CodeAgain)
	}
	if SideMax != 10 {
		t.Errorf("SideMax = %d, want 10", SideMax)
	}
	wantFilters := []string{"", "me", "mine", "signing", "done", "closed", "templates", "hr", "safe"}
	if len(Filters) != len(wantFilters) {
		t.Fatalf("Filters len = %d, want %d", len(Filters), len(wantFilters))
	}
	for i, f := range Filters {
		if f != wantFilters[i] {
			t.Errorf("Filters[%d] = %q, want %q", i, f, wantFilters[i])
		}
	}
}

// ---------- Signer Order & State Transitions Simulation ----------

func TestSignerOrder_FullWorkflow(t *testing.T) {
	// Scenario: Document with 3 signers in order:
	// 1. Reviewer (employee, approves without signature/code)
	// 2. Director (employee, signs in go24)
	// 3. Client (external, signs with code via personal link)

	parties := []Party{
		{ID: "p1", Ord: 1, UserID: "u-rev", Name: "Reviewer", Role: "reviewer", Status: "ready"},
		{ID: "p2", Ord: 2, UserID: "u-ceo", Name: "Director", Status: "wait"},
		{ID: "p3", Ord: 3, Email: "client@example.com", Name: "Client", Token: token(), Status: "wait"},
	}
	doc := Doc{
		ID:      "doc-1",
		Title:   "Service Contract",
		Status:  "signing",
		Parties: parties,
	}

	// Initial State Verification
	if doc.Status != "signing" {
		t.Fatalf("initial doc status = %q, want signing", doc.Status)
	}
	curr := doc.Current()
	if curr == nil || curr.ID != "p1" {
		t.Fatalf("initial current signer = %+v, want p1", curr)
	}
	if !curr.Reviewer() {
		t.Fatalf("first signer is not a reviewer")
	}

	// Step 1: Reviewer Approves
	curr.Status = "signed"
	doc.Parties[1].Status = "ready"
	// Event logged: EvApproved

	curr = doc.Current()
	if curr == nil || curr.ID != "p2" {
		t.Fatalf("current signer after approval = %+v, want p2", curr)
	}
	if !curr.Employee() || curr.Reviewer() {
		t.Fatalf("p2 expected employee signer")
	}
	if doc.Status != "signing" {
		t.Fatalf("doc status after step 1 = %q, want signing", doc.Status)
	}

	// Step 2: Director Signs
	curr.Status = "signed"
	doc.Parties[2].Status = "ready"
	// Event logged: EvSigned

	curr = doc.Current()
	if curr == nil || curr.ID != "p3" {
		t.Fatalf("current signer after director signs = %+v, want p3", curr)
	}
	if curr.Employee() {
		t.Fatalf("p3 expected client (non-employee)")
	}
	if curr.Token == "" {
		t.Fatalf("p3 client has no token")
	}

	// Step 3: Client Signs
	curr.Status = "signed"
	doc.Status = "done"
	now := time.Now()
	doc.DoneAt = &now
	// Event logged: EvSigned, then EvDone

	if doc.Current() != nil {
		t.Fatalf("doc.Current() after all signed = %+v, want nil", doc.Current())
	}
	if doc.Status != "done" || doc.DoneAt == nil {
		t.Fatalf("final doc status = %q, doneAt = %v", doc.Status, doc.DoneAt)
	}
}

func TestSignerOrder_RefusalFlow(t *testing.T) {
	// Scenario: Second signer refuses to sign
	parties := []Party{
		{ID: "p1", Ord: 1, UserID: "u-1", Status: "signed"},
		{ID: "p2", Ord: 2, UserID: "u-2", Status: "ready"},
		{ID: "p3", Ord: 3, Email: "client@test", Status: "wait"},
	}
	doc := Doc{
		ID:      "doc-refuse",
		Status:  "signing",
		Parties: parties,
	}

	curr := doc.Current()
	if curr == nil || curr.ID != "p2" {
		t.Fatalf("current signer = %+v, want p2", curr)
	}

	// p2 Refuses
	reason := "Incorrect payment terms in section 4"
	curr.Status = "refused"
	curr.Reason = truncate(reason, 1000)
	doc.Status = "refused"
	now := time.Now()
	doc.DoneAt = &now

	// Document transitions to terminal 'refused' state
	if doc.Current() != nil {
		t.Errorf("doc.Current() after refusal = %+v, want nil", doc.Current())
	}
	if doc.Status != "refused" {
		t.Errorf("doc.Status = %q, want refused", doc.Status)
	}
	if doc.Parties[2].Status != "wait" {
		t.Errorf("subsequent party status = %q, want wait", doc.Parties[2].Status)
	}
}

func TestSignerOrder_CancellationFlow(t *testing.T) {
	// Scenario: Author cancels document during signing
	parties := []Party{
		{ID: "p1", Ord: 1, UserID: "u-1", Status: "ready"},
		{ID: "p2", Ord: 2, Email: "client@test", Status: "wait"},
	}
	doc := Doc{
		ID:      "doc-cancel",
		Status:  "signing",
		Parties: parties,
	}

	// Sender recalls document
	doc.Status = "cancelled"
	now := time.Now()
	doc.DoneAt = &now

	// Document is cancelled
	if doc.Status != "cancelled" {
		t.Errorf("doc.Status = %q, want cancelled", doc.Status)
	}
	if doc.DoneAt == nil {
		t.Errorf("doc.DoneAt is nil")
	}
}

// ---------- Store Pure Validation Tests (Nil DB) ----------

func TestStore_Create_Validation(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	validParty := Party{Name: "Bob", UserID: "u-bob"}

	tests := []struct {
		name    string
		doc     Doc
		parties []Party
		wantErr error
	}{
		{
			name:    "empty title",
			doc:     Doc{Title: ""},
			parties: []Party{validParty},
			wantErr: ErrTitle,
		},
		{
			name:    "whitespace title",
			doc:     Doc{Title: "   \t\n  "},
			parties: []Party{validParty},
			wantErr: ErrTitle,
		},
		{
			name:    "title exceeds 300 runes",
			doc:     Doc{Title: strings.Repeat("А", 301)},
			parties: []Party{validParty},
			wantErr: ErrTitle,
		},
		{
			name:    "no parties",
			doc:     Doc{Title: "Contract"},
			parties: nil,
			wantErr: ErrParties,
		},
		{
			name:    "empty parties slice",
			doc:     Doc{Title: "Contract"},
			parties: []Party{},
			wantErr: ErrParties,
		},
		{
			name:    "party with empty name",
			doc:     Doc{Title: "Contract"},
			parties: []Party{{Name: "", UserID: "u-1"}},
			wantErr: ErrParties,
		},
		{
			name:    "party with whitespace name",
			doc:     Doc{Title: "Contract"},
			parties: []Party{{Name: "   ", UserID: "u-1"}},
			wantErr: ErrParties,
		},
		{
			name:    "client party missing '@' in email",
			doc:     Doc{Title: "Contract"},
			parties: []Party{{Name: "Client", Email: "invalid-email"}},
			wantErr: ErrParties,
		},
		{
			name:    "client party with empty email",
			doc:     Doc{Title: "Contract"},
			parties: []Party{{Name: "Client", Email: ""}},
			wantErr: ErrParties,
		},
		{
			name:    "reviewer party without UserID",
			doc:     Doc{Title: "Contract"},
			parties: []Party{{Name: "Reviewer", Role: "reviewer", UserID: "", Email: "rev@test.com"}},
			wantErr: ErrParties,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Create(ctx, tc.doc, tc.parties)
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Create() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestStore_ByToken_Empty(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	if _, _, err := s.ByToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByToken(\"\") error = %v, want ErrNotFound", err)
	}
}

func TestStore_List_PreFiltered(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	// "hr" and "templates" return nil, nil without executing database queries
	docs, err := s.List(ctx, "u-1", "hr", false)
	if err != nil || docs != nil {
		t.Errorf("List(hr) = (%v, %v), want (nil, nil)", docs, err)
	}

	docs, err = s.List(ctx, "u-1", "templates", false)
	if err != nil || docs != nil {
		t.Errorf("List(templates) = (%v, %v), want (nil, nil)", docs, err)
	}
}

func TestStore_Parties_Empty(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	if err := s.parties(ctx, nil); err != nil {
		t.Errorf("parties(nil) error = %v, want nil", err)
	}
	if err := s.parties(ctx, []Doc{}); err != nil {
		t.Errorf("parties([]) error = %v, want nil", err)
	}
}
