package marketing

import (
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"
)

func TestValidEntity(t *testing.T) {
	tests := []struct {
		entity string
		want   bool
	}{
		{"contact", true},
		{"company", true},
		{"lead", true},
		{"list", true},
		{"", false},
		{"deal", false},
		{"deal_category", false},
		{"activity", false},
		{"user", false},
		{"CONTACT", false},
		{"list ", false},
		{" lead", false},
		{"segment", false},
	}
	for _, tc := range tests {
		if got := validEntity(tc.entity); got != tc.want {
			t.Errorf("validEntity(%q) = %v, want %v", tc.entity, got, tc.want)
		}
	}
}

func TestValidEmail(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"alice@example.com", true},
		{"bob+tag@sub.domain.co.uk", true},
		{"user.name@test.org", true},
		{"a@b.c", true},
		{"123@digits.io", true},
		{"first_last-1@domain.net", true},
		{"", false},
		{"plainaddress", false},
		{"@missinguser.com", false},
		{"missingdomain@", false},
		{"missingtld@domain", false},
		{"Alice <alice@example.com>", false},
		{"<alice@example.com>", false},
		{"user@exam ple.com", false},
		{"user @example.com", false},
		{"user@@example.com", false},
		{"user@domain..com", false},
		{"   ", false},
	}
	for _, tc := range tests {
		if got := ValidEmail(tc.addr); got != tc.want {
			t.Errorf("ValidEmail(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestParseList(t *testing.T) {
	input := `
		Иван Иванов <ivan@example.com>
		alice@example.com;Алиса
		bob@example.com,Боб
		carol@example.com	"Кэрол Смит"
		dave@example.com
		<bracket@example.com>
		IVAN@example.com;Иван Дубликат
		invalid-email
		;only-name
		eve@example.com
	`
	got := ParseList(input)
	want := []Recipient{
		{Email: "ivan@example.com", Name: "Иван Иванов"},
		{Email: "alice@example.com", Name: "Алиса"},
		{Email: "bob@example.com", Name: "Боб"},
		{Email: "carol@example.com", Name: "Кэрол Смит"},
		{Email: "dave@example.com", Name: ""},
		{Email: "bracket@example.com", Name: ""},
		{Email: "eve@example.com", Name: ""},
	}

	if len(got) != len(want) {
		t.Fatalf("ParseList returned %d items, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseListEdgeCases(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		if got := ParseList(""); len(got) != 0 {
			t.Errorf("ParseList(\"\") = %+v, want empty", got)
		}
		if got := ParseList("   \n\r\n\t  "); len(got) != 0 {
			t.Errorf("ParseList(whitespace) = %+v, want empty", got)
		}
	})

	t.Run("crlf and cr line endings", func(t *testing.T) {
		raw := "a@test.com;UserA\r\nb@test.com;UserB\rc@test.com;UserC\n"
		got := ParseList(raw)
		if len(got) != 3 {
			t.Fatalf("ParseList line endings: got %d, want 3", len(got))
		}
		if got[0].Email != "a@test.com" || got[1].Email != "b@test.com" || got[2].Email != "c@test.com" {
			t.Errorf("unexpected recipients: %+v", got)
		}
	})

	t.Run("case-insensitive deduplication", func(t *testing.T) {
		raw := "User@Example.Com;First\nuser@example.com;Second\nUSER@EXAMPLE.COM;Third"
		got := ParseList(raw)
		if len(got) != 1 {
			t.Fatalf("expected 1 recipient, got %d: %+v", len(got), got)
		}
		if got[0].Email != "User@Example.Com" || got[0].Name != "First" {
			t.Errorf("expected first occurrence to be preserved, got %+v", got[0])
		}
	})

	t.Run("skips invalid lines without emails", func(t *testing.T) {
		raw := "not-an-email;John\n@no-user.com\nhello@world\nvalid@domain.com"
		got := ParseList(raw)
		if len(got) != 1 || got[0].Email != "valid@domain.com" {
			t.Fatalf("expected only valid email, got %+v", got)
		}
	})
}

func TestToken(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		tok := token()
		if len(tok) != 32 {
			t.Fatalf("token length = %d, want 32", len(tok))
		}
		b, err := hex.DecodeString(tok)
		if err != nil {
			t.Fatalf("token %q is not valid hex: %v", tok, err)
		}
		if len(b) != 16 {
			t.Fatalf("decoded byte slice length = %d, want 16", len(b))
		}
		if seen[tok] {
			t.Fatalf("token %q generated duplicate", tok)
		}
		seen[tok] = true
	}
}

func TestFilterJSON(t *testing.T) {
	t.Run("empty filter marshals to empty object", func(t *testing.T) {
		var f Filter
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(b) != "{}" {
			t.Errorf("empty Filter = %s, want {}", string(b))
		}
	})

	t.Run("full filter roundtrip", func(t *testing.T) {
		f := Filter{
			Q:           "search query",
			Responsible: "resp-123",
			Source:      "src-456",
			Stage:       "stg-789",
			Semantic:    "success",
			From:        "2026-01-01",
			To:          "2026-12-31",
		}
		b, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var got Filter
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if got != f {
			t.Errorf("Filter roundtrip mismatch: got %+v, want %+v", got, f)
		}
	})

	t.Run("date parsing validation", func(t *testing.T) {
		f := Filter{From: "2026-05-10", To: "2026-05-20"}
		for _, d := range []string{f.From, f.To} {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				t.Errorf("date %q should parse as DateOnly: %v", d, err)
			}
		}
		invalid := "10.05.2026"
		if _, err := time.Parse(time.DateOnly, invalid); err == nil {
			t.Errorf("time.Parse DateOnly unexpectedly succeeded for %q", invalid)
		}
	})
}
