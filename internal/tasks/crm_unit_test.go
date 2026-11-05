package tasks

import (
	"strings"
	"testing"
)

func TestParseCRM_Unit(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		wantKind string
		wantID   int64
		wantOwn  bool
		wantErr  bool
	}{
		// the portal deal
		{
			name:     "legacy deal",
			raw:      "https://portal.example.com/crm/deal/details/123/",
			wantKind: "deal",
			wantID:   123,
			wantOwn:  false,
			wantErr:  false,
		},
		// the portal contact
		{
			name:     "legacy contact",
			raw:      "https://portal.example.com/crm/contact/details/456/",
			wantKind: "contact",
			wantID:   456,
			wantOwn:  false,
			wantErr:  false,
		},
		// the portal company
		{
			name:     "legacy company",
			raw:      "https://portal.example.com/crm/company/details/789/",
			wantKind: "company",
			wantID:   789,
			wantOwn:  false,
			wantErr:  false,
		},
		// the portal lead
		{
			name:     "legacy lead",
			raw:      "https://portal.example.com/crm/lead/details/101/",
			wantKind: "lead",
			wantID:   101,
			wantOwn:  false,
			wantErr:  false,
		},
		// the portal quote
		{
			name:     "legacy quote",
			raw:      "https://portal.example.com/crm/quote/details/102/",
			wantKind: "quote",
			wantID:   102,
			wantOwn:  false,
			wantErr:  false,
		},
		// the portal invoice
		{
			name:     "legacy invoice",
			raw:      "https://portal.example.com/crm/invoice/details/103/",
			wantKind: "invoice",
			wantID:   103,
			wantOwn:  false,
			wantErr:  false,
		},
		// the portal smart process item
		{
			name:     "legacy smart process item",
			raw:      "https://portal.example.com/crm/type/177/details/5/",
			wantKind: "item",
			wantID:   5,
			wantOwn:  false,
			wantErr:  false,
		},
		// Generic external URL
		{
			name:     "generic external url",
			raw:      "https://example.org/any/path?param=1",
			wantKind: "other",
			wantID:   0,
			wantOwn:  false,
			wantErr:  false,
		},
		// Own go24 CRM deals
		{
			name:     "own deal",
			raw:      "/crm/deals/a1b2c3d4-e5f6-7a8b-9c0d-1e2f3a4b5c6d",
			wantKind: "deal",
			wantID:   0,
			wantOwn:  true,
			wantErr:  false,
		},
		// Own go24 CRM leads
		{
			name:     "own lead",
			raw:      "/crm/leads/11111111-2222-3333-4444-555555555555",
			wantKind: "lead",
			wantID:   0,
			wantOwn:  true,
			wantErr:  false,
		},
		// Own go24 CRM contacts
		{
			name:     "own contact",
			raw:      "/crm/contacts/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			wantKind: "contact",
			wantID:   0,
			wantOwn:  true,
			wantErr:  false,
		},
		// Own go24 CRM companies
		{
			name:     "own company",
			raw:      "/crm/companies/99999999-8888-7777-6666-555555555555",
			wantKind: "company",
			wantID:   0,
			wantOwn:  true,
			wantErr:  false,
		},
		// Invalid schemes
		{
			name:    "ftp scheme",
			raw:     "ftp://example.com/crm/deal/details/1/",
			wantErr: true,
		},
		{
			name:    "javascript scheme",
			raw:     "javascript:alert(1)",
			wantErr: true,
		},
		// Missing host
		{
			name:    "http missing host",
			raw:     "http:///crm/deal/details/1/",
			wantErr: true,
		},
		// Too long URL (> 1000 bytes)
		{
			name:    "too long url",
			raw:     "https://example.com/" + strings.Repeat("a", 1001),
			wantErr: true,
		},
		// Relative path without /crm/... uuid
		{
			name:    "invalid relative path",
			raw:     "crm/deal/details/1",
			wantErr: true,
		},
		{
			name:    "empty string",
			raw:     "   ",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseCRM(tc.raw)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseCRM(%q) error = %v, wantErr %v", tc.raw, err, tc.wantErr)
			}
			if !tc.wantErr {
				if got.Kind != tc.wantKind || got.EntityID != tc.wantID || got.Own != tc.wantOwn {
					t.Errorf("ParseCRM(%q) = %+v, want kind=%q id=%d own=%v", tc.raw, got, tc.wantKind, tc.wantID, tc.wantOwn)
				}
			}
		})
	}
}
