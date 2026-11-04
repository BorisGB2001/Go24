package crm

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestReadCSV_Unit(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantRows int
		wantCols int
		wantCell string // cell at [0][0]
		wantErr  bool
		errKey   string
	}{
		{
			name:     "semicolon separated",
			input:    "Name;Phone;Email\nAlice;+7900;alice@t\nBob;+7901;bob@t",
			wantRows: 3,
			wantCols: 3,
			wantCell: "Name",
		},
		{
			name:     "comma separated",
			input:    "title,amount,company\nDeal 1,100,Acme\nDeal 2,200,Beta",
			wantRows: 3,
			wantCols: 3,
			wantCell: "title",
		},
		{
			name:     "tab separated",
			input:    "id\tname\tstatus\n1\tItem 1\tnew\n2\tItem 2\tdone",
			wantRows: 3,
			wantCols: 3,
			wantCell: "id",
		},
		{
			name:     "utf8 bom stripped",
			input:    "\ufeffName;Value\nTest;123",
			wantRows: 2,
			wantCols: 2,
			wantCell: "Name",
		},
		{
			name:     "quotes and multiline",
			input:    "header1;header2\n\"line 1\nline 2\";value2",
			wantRows: 2,
			wantCols: 2,
			wantCell: "header1",
		},
		{
			name:    "invalid utf8 bytes return error",
			input:   "header1,header2\n\xff\xfe\x00\x00,val",
			wantErr: true,
			errKey:  "err.crm_csv_encoding",
		},
		{
			name:     "empty input",
			input:    "",
			wantRows: 0,
			wantCols: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := ReadCSV(strings.NewReader(tt.input))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReadCSV() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				var ie *i18n.Error
				if !errors.As(err, &ie) || ie.Key != tt.errKey {
					t.Errorf("ReadCSV() expected error key %q, got %v", tt.errKey, err)
				}
				return
			}
			if len(rows) != tt.wantRows {
				t.Fatalf("ReadCSV() row count = %d, want %d", len(rows), tt.wantRows)
			}
			if tt.wantRows > 0 {
				if len(rows[0]) != tt.wantCols {
					t.Errorf("ReadCSV() col count = %d, want %d", len(rows[0]), tt.wantCols)
				}
				if rows[0][0] != tt.wantCell {
					t.Errorf("ReadCSV() cell[0][0] = %q, want %q", rows[0][0], tt.wantCell)
				}
			}
		})
	}
}

func TestExportName_Unit(t *testing.T) {
	tm := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	got := ExportName("deals", tm)
	want := "crm-deals-2026-10-28.csv"
	if got != want {
		t.Errorf("ExportName() = %q, want %q", got, want)
	}

	gotLead := ExportName("leads", tm)
	wantLead := "crm-leads-2026-10-28.csv"
	if gotLead != wantLead {
		t.Errorf("ExportName() = %q, want %q", gotLead, wantLead)
	}
}

func TestImportColumns_Unit(t *testing.T) {
	aliases := map[string]string{
		"name":         "name",
		"имя":          "name",
		"фио":          "name",
		"title":        "title",
		"название":     "title",
		"компания":     "company",
		"phone":        "phone",
		"телефон":      "phone",
		"email":        "email",
		"почта":        "email",
		"source":       "origin",
		"источник":     "origin",
		"contact type": "contact_type",
		"тип контакта": "contact_type",
		"amount":       "amount",
		"сумма":        "amount",
		"currency":     "currency",
		"валюта":       "currency",
		"comment":      "comment",
		"комментарий":  "comment",
	}

	for alias, expected := range aliases {
		if got := ImportColumns[alias]; got != expected {
			t.Errorf("ImportColumns[%q] = %q, want %q", alias, got, expected)
		}
	}
}
