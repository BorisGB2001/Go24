package telephony

import (
	"testing"
	"time"
)

func TestCDRStatus(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		cdr  CDR
		want string
	}{
		{
			name: "voicemail with record",
			cdr:  CDR{Voicemail: true, Record: "msg.wav", Dial: "ANSWER"},
			want: StatusVoicemail,
		},
		{
			name: "voicemail without record",
			cdr:  CDR{Voicemail: true, Record: "", Dial: "ANSWER", Answer: t0},
			want: StatusNoAnswer,
		},
		{
			name: "answered by timestamp",
			cdr:  CDR{Answer: t0, Dial: "NOANSWER"},
			want: StatusAnswered,
		},
		{
			name: "answered by dial status",
			cdr:  CDR{Dial: "ANSWER"},
			want: StatusAnswered,
		},
		{
			name: "busy",
			cdr:  CDR{Dial: "BUSY"},
			want: StatusBusy,
		},
		{
			name: "no answer",
			cdr:  CDR{Dial: "NOANSWER"},
			want: StatusNoAnswer,
		},
		{
			name: "cancel",
			cdr:  CDR{Dial: "CANCEL"},
			want: StatusCancel,
		},
		{
			name: "congestion failed",
			cdr:  CDR{Dial: "CONGESTION"},
			want: StatusFailed,
		},
		{
			name: "chanunavail failed",
			cdr:  CDR{Dial: "CHANUNAVAIL"},
			want: StatusFailed,
		},
		{
			name: "empty dial status failed",
			cdr:  CDR{Dial: ""},
			want: StatusFailed,
		},
		{
			name: "unknown dial status failed",
			cdr:  CDR{Dial: "OTHER"},
			want: StatusFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cdr.Status(); got != tc.want {
				t.Errorf("CDR(%+v).Status() = %q, want %q", tc.cdr, got, tc.want)
			}
		})
	}
}

func TestCallOutside(t *testing.T) {
	tests := []struct {
		name     string
		call     Call
		wantNum  string
		wantDir  string
		wantUser string
	}{
		{
			name: "incoming from outside",
			call: Call{
				From:     "+79001234567",
				To:       "101",
				FromUser: Who{},
				ToUser:   Who{UserID: "u1", Name: "Ann"},
			},
			wantNum:  "+79001234567",
			wantDir:  "in",
			wantUser: "u1",
		},
		{
			name: "outgoing to outside",
			call: Call{
				From:     "101",
				To:       "+74950001122",
				FromUser: Who{UserID: "u1", Name: "Ann"},
				ToUser:   Who{},
			},
			wantNum:  "+74950001122",
			wantDir:  "out",
			wantUser: "u1",
		},
		{
			name: "internal between employees",
			call: Call{
				From:     "101",
				To:       "102",
				FromUser: Who{UserID: "u1", Name: "Ann"},
				ToUser:   Who{UserID: "u2", Name: "Bob"},
			},
			wantNum:  "",
			wantDir:  "",
			wantUser: "",
		},
		{
			name: "neither is employee",
			call: Call{
				From:     "+79001112233",
				To:       "+74950001122",
				FromUser: Who{},
				ToUser:   Who{},
			},
			wantNum:  "",
			wantDir:  "",
			wantUser: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			num, dir, emp := tc.call.Outside()
			if num != tc.wantNum || dir != tc.wantDir || emp != tc.wantUser {
				t.Errorf("Call.Outside() = (%q, %q, %q), want (%q, %q, %q)",
					num, dir, emp, tc.wantNum, tc.wantDir, tc.wantUser)
			}
		})
	}
}

func TestCallMissed(t *testing.T) {
	for status, want := range map[string]bool{
		StatusAnswered:  false,
		StatusNoAnswer:  true,
		StatusBusy:      true,
		StatusCancel:    true,
		StatusFailed:    true,
		StatusVoicemail: true,
		"":              true,
	} {
		c := Call{Status: status}
		if got := c.Missed(); got != want {
			t.Errorf("Call{Status: %q}.Missed() = %v, want %v", status, got, want)
		}
	}
}

func TestCallDuration(t *testing.T) {
	for secs, want := range map[int]string{
		0:     "0:00",
		5:     "0:05",
		59:    "0:59",
		60:    "1:00",
		75:    "1:15",
		605:   "10:05",
		3599:  "59:59",
		3600:  "1:00:00",
		3605:  "1:00:05",
		3661:  "1:01:01",
		7200:  "2:00:00",
		36065: "10:01:05",
	} {
		c := Call{Seconds: secs}
		if got := c.Duration(); got != want {
			t.Errorf("Call{Seconds: %d}.Duration() = %q, want %q", secs, got, want)
		}
	}
}

func TestCallCRMPath(t *testing.T) {
	for _, tc := range []struct {
		entity string
		crmID  string
		want   string
	}{
		{"deal", "123", "/crm/deals/123"},
		{"lead", "456", "/crm/leads/456"},
		{"contact", "789", "/crm/contacts/789"},
		{"company", "101", "/crm/companies/101"},
		{"deal", "", ""},
		{"", "123", ""},
		{"unknown", "123", ""},
	} {
		c := Call{CRMEntity: tc.entity, CRMID: tc.crmID}
		if got := c.CRMPath(); got != tc.want {
			t.Errorf("Call{Entity: %q, ID: %q}.CRMPath() = %q, want %q", tc.entity, tc.crmID, got, tc.want)
		}
	}
}
