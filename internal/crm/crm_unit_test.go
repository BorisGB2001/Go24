package crm

import (
	"errors"
	"reflect"
	"testing"

	"go24/internal/i18n"
)

func TestAmount_Unit(t *testing.T) {
	tests := []struct {
		v    float64
		want string
	}{
		{0, "0"},
		{0.0, "0"},
		{-0.0, "0"},
		{-0.0001, "0"},
		{5, "5"},
		{999, "999"},
		{1000, "1 000"},
		{1500000, "1 500 000"},
		{1234.5, "1 234,50"},
		{12.05, "12,05"},
		{0.99, "0,99"},
		{0.01, "0,01"},
		{-1234.5, "−1 234,50"},
		{-1500000, "−1 500 000"},
		{-12.05, "−12,05"},
		{-0.5, "−0,50"},
		{1000000000.75, "1 000 000 000,75"},
	}

	for _, tt := range tests {
		got := Amount(tt.v)
		if got != tt.want {
			t.Errorf("Amount(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func TestClean_Unit(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		max     int
		wantStr string
		wantErr bool
	}{
		{
			name:    "trims whitespace",
			input:   "  hello world  \n\t",
			max:     20,
			wantStr: "hello world",
			wantErr: false,
		},
		{
			name:    "exact length",
			input:   "abcde",
			max:     5,
			wantStr: "abcde",
			wantErr: false,
		},
		{
			name:    "exceeds length",
			input:   "abcdef",
			max:     5,
			wantStr: "abcdef",
			wantErr: true,
		},
		{
			name:    "unicode runes within length",
			input:   "  Привет  ",
			max:     6,
			wantStr: "Привет",
			wantErr: false,
		},
		{
			name:    "unicode runes exceeding length",
			input:   "Привет мир",
			max:     5,
			wantStr: "Привет мир",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "   ",
			max:     10,
			wantStr: "",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.input
			err := clean(&s, tt.max)
			if s != tt.wantStr {
				t.Errorf("clean() string = %q, want %q", s, tt.wantStr)
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("clean() error = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != "err.crm_field" {
					t.Errorf("expected err.crm_field, got %v", err)
				}
			}
		})
	}
}

func TestLike_Unit(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"abc", "%abc%"},
		{"  abc  ", "%abc%"},
		{"", "%%"},
		{"100%", "%100\\%%"},
		{"user_name", "%user\\_name%"},
		{"c:\\path\\file", "%c:\\\\path\\\\file%"},
		{"%_\\", "%\\%\\_\\\\%"},
	}
	for _, c := range cases {
		got := like(c.in)
		if got != c.want {
			t.Errorf("like(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOptional_Unit(t *testing.T) {
	if got := optional(""); got != nil {
		t.Errorf("optional(\"\") = %v, want nil", got)
	}
	s := "123"
	if got := optional(s); got == nil || *got != s {
		t.Errorf("optional(%q) = %v, want pointer to %q", s, got, s)
	}
}

func TestStageText_Unit(t *testing.T) {
	tests := []struct {
		name  string
		stage Stage
		want  string
	}{
		{
			name:  "built-in stage without custom name",
			stage: Stage{Code: "new", Name: ""},
			want:  "code:new",
		},
		{
			name:  "stage with name and code",
			stage: Stage{Code: "new", Name: "Новый"},
			want:  "Новый",
		},
		{
			name:  "custom stage without code",
			stage: Stage{Code: "", Name: "На согласовании"},
			want:  "На согласовании",
		},
		{
			name:  "empty stage",
			stage: Stage{},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stageText(tt.stage); got != tt.want {
				t.Errorf("stageText(%+v) = %q, want %q", tt.stage, got, tt.want)
			}
		})
	}
}

func TestDealContacts_Unit(t *testing.T) {
	tests := []struct {
		name string
		deal Deal
		want []string
	}{
		{
			name: "contacts nil and single contact present",
			deal: Deal{Contact: Ref{ID: "c1"}},
			want: []string{"c1"},
		},
		{
			name: "contacts nil and single contact empty",
			deal: Deal{},
			want: []string{},
		},
		{
			name: "contacts with duplicates and blanks",
			deal: Deal{
				Contact: Ref{ID: "ignored"},
				Contacts: []Ref{
					{ID: "c1"},
					{ID: ""},
					{ID: "c2"},
					{ID: "c1"},
					{ID: "c3"},
					{ID: "c2"},
				},
			},
			want: []string{"c1", "c2", "c3"},
		},
		{
			name: "empty contacts slice",
			deal: Deal{Contacts: []Ref{}},
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dealContacts(&tt.deal)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("dealContacts() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStageClosed_Unit(t *testing.T) {
	tests := []struct {
		semantic string
		want     bool
	}{
		{"process", false},
		{"success", true},
		{"failure", true},
		{"closed", true},
		{"", true},
	}

	for _, tt := range tests {
		s := Stage{Semantic: tt.semantic}
		if got := s.Closed(); got != tt.want {
			t.Errorf("Stage{Semantic: %q}.Closed() = %v, want %v", tt.semantic, got, tt.want)
		}
	}
}

func TestLeadConverted_Unit(t *testing.T) {
	tests := []struct {
		name string
		lead Lead
		want bool
	}{
		{
			name: "lead with deal id",
			lead: Lead{DealID: "d-1"},
			want: true,
		},
		{
			name: "lead in success stage",
			lead: Lead{Stage: Stage{Semantic: "success"}},
			want: true,
		},
		{
			name: "non-repeat lead with contact id",
			lead: Lead{Repeat: false, ContactID: "c-1"},
			want: true,
		},
		{
			name: "non-repeat lead with company id",
			lead: Lead{Repeat: false, CompanyID: "co-1"},
			want: true,
		},
		{
			name: "repeat lead with contact id is not converted",
			lead: Lead{Repeat: true, ContactID: "c-1", Stage: Stage{Semantic: "process"}},
			want: false,
		},
		{
			name: "repeat lead with company id is not converted",
			lead: Lead{Repeat: true, CompanyID: "co-1", Stage: Stage{Semantic: "process"}},
			want: false,
		},
		{
			name: "fresh lead",
			lead: Lead{Stage: Stage{Semantic: "process"}},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.lead.Converted(); got != tt.want {
				t.Errorf("Lead.Converted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMailFrom_Unit(t *testing.T) {
	tests := []struct {
		name string
		mail Mail
		want string
	}{
		{
			name: "prefers from name",
			mail: Mail{FromName: "Alice", FromAddr: "alice@example.com"},
			want: "Alice",
		},
		{
			name: "falls back to from addr",
			mail: Mail{FromName: "", FromAddr: "bob@example.com"},
			want: "bob@example.com",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.mail.From(); got != tt.want {
				t.Errorf("Mail.From() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInnSQL_Unit(t *testing.T) {
	sql := innSQL("deal", "d")
	want := `EXISTS (SELECT 1 FROM crm_requisites rq WHERE rq.entity = 'deal' AND rq.entity_id = d.id AND rq.inn ILIKE $1)`
	if sql != want {
		t.Errorf("innSQL() = %q, want %q", sql, want)
	}
}
