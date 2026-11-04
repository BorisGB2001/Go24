package marketing

import (
	"testing"
	"time"
)

func TestCampaignAB(t *testing.T) {
	tests := []struct {
		name string
		c    Campaign
		want bool
	}{
		{
			name: "zero share",
			c:    Campaign{ABShare: 0, ABSubject: "Subject B", ABBody: "Body B"},
			want: false,
		},
		{
			name: "negative share",
			c:    Campaign{ABShare: -10, ABSubject: "Subject B"},
			want: false,
		},
		{
			name: "positive share with subject and body",
			c:    Campaign{ABShare: 50, ABSubject: "Subject B", ABBody: "Body B"},
			want: true,
		},
		{
			name: "positive share with subject only",
			c:    Campaign{ABShare: 20, ABSubject: "Subject B"},
			want: true,
		},
		{
			name: "positive share with body only",
			c:    Campaign{ABShare: 30, ABBody: "Body B"},
			want: true,
		},
		{
			name: "positive share with empty variants",
			c:    Campaign{ABShare: 50, ABSubject: "", ABBody: ""},
			want: false,
		},
		{
			name: "positive share with whitespace body only",
			c:    Campaign{ABShare: 50, ABSubject: "", ABBody: "   \t\n  "},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.AB(); got != tc.want {
				t.Errorf("Campaign.AB() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCampaignVariant(t *testing.T) {
	orig := Campaign{
		Subject:   "Subject A",
		Body:      "Body A",
		ABSubject: "Subject B",
		ABBody:    "Body B",
	}

	t.Run("variant a returns original", func(t *testing.T) {
		v := orig.Variant("a")
		if v.Subject != "Subject A" || v.Body != "Body A" {
			t.Errorf("Variant(\"a\") = {%q, %q}, want {%q, %q}", v.Subject, v.Body, "Subject A", "Body A")
		}
	})

	t.Run("empty or unknown variant returns original", func(t *testing.T) {
		for _, variantName := range []string{"", "other", "c"} {
			v := orig.Variant(variantName)
			if v.Subject != "Subject A" || v.Body != "Body A" {
				t.Errorf("Variant(%q) = {%q, %q}, want original", variantName, v.Subject, v.Body)
			}
		}
	})

	t.Run("variant b replaces both subject and body", func(t *testing.T) {
		v := orig.Variant("b")
		if v.Subject != "Subject B" || v.Body != "Body B" {
			t.Errorf("Variant(\"b\") = {%q, %q}, want {%q, %q}", v.Subject, v.Body, "Subject B", "Body B")
		}
		// Ensure value receiver didn't mutate orig
		if orig.Subject != "Subject A" || orig.Body != "Body A" {
			t.Errorf("original campaign was mutated: %+v", orig)
		}
	})

	t.Run("variant b with empty subject keeps original subject", func(t *testing.T) {
		c := Campaign{Subject: "Original Subject", Body: "Original Body", ABBody: "New Body"}
		v := c.Variant("b")
		if v.Subject != "Original Subject" || v.Body != "New Body" {
			t.Errorf("Variant(\"b\") = {%q, %q}, want {%q, %q}", v.Subject, v.Body, "Original Subject", "New Body")
		}
	})

	t.Run("variant b with empty or whitespace body keeps original body", func(t *testing.T) {
		c := Campaign{Subject: "Original Subject", Body: "Original Body", ABSubject: "New Subject", ABBody: "   "}
		v := c.Variant("b")
		if v.Subject != "New Subject" || v.Body != "Original Body" {
			t.Errorf("Variant(\"b\") = {%q, %q}, want {%q, %q}", v.Subject, v.Body, "New Subject", "Original Body")
		}
	})
}

func TestCampaignABDue(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	sendAt := now.Add(2 * time.Hour)
	startedAt := now.Add(-1 * time.Hour)

	t.Run("sendAt takes precedence", func(t *testing.T) {
		c := Campaign{
			SendAt:    &sendAt,
			StartedAt: &startedAt,
			ABHours:   4,
		}
		want := sendAt.Add(4 * time.Hour)
		if got := c.ABDue(); !got.Equal(want) {
			t.Errorf("ABDue() = %v, want %v", got, want)
		}
	})

	t.Run("startedAt used when sendAt is nil", func(t *testing.T) {
		c := Campaign{
			SendAt:    nil,
			StartedAt: &startedAt,
			ABHours:   6,
		}
		want := startedAt.Add(6 * time.Hour)
		if got := c.ABDue(); !got.Equal(want) {
			t.Errorf("ABDue() = %v, want %v", got, want)
		}
	})

	t.Run("fallback to now when both sendAt and startedAt are nil", func(t *testing.T) {
		c := Campaign{
			SendAt:    nil,
			StartedAt: nil,
			ABHours:   3,
		}
		before := time.Now().Add(3 * time.Hour)
		got := c.ABDue()
		after := time.Now().Add(3 * time.Hour)

		if got.Before(before.Add(-time.Second)) || got.After(after.Add(time.Second)) {
			t.Errorf("ABDue() = %v, want roughly between %v and %v", got, before, after)
		}
	})
}

func TestCampaignTesting(t *testing.T) {
	tests := []struct {
		name string
		c    Campaign
		want bool
	}{
		{
			name: "not AB campaign",
			c:    Campaign{ABShare: 0, Status: "sending"},
			want: false,
		},
		{
			name: "AB campaign with winner already chosen",
			c:    Campaign{ABShare: 20, ABSubject: "B", ABWinner: "a", Status: "sending"},
			want: false,
		},
		{
			name: "active test in sending status",
			c:    Campaign{ABShare: 20, ABSubject: "B", ABWinner: "", Status: "sending"},
			want: true,
		},
		{
			name: "active test in paused status",
			c:    Campaign{ABShare: 20, ABBody: "Body B", ABWinner: "", Status: "paused"},
			want: true,
		},
		{
			name: "draft status is not testing",
			c:    Campaign{ABShare: 50, ABSubject: "B", Status: "draft"},
			want: false,
		},
		{
			name: "done status is not testing",
			c:    Campaign{ABShare: 50, ABSubject: "B", Status: "done"},
			want: false,
		},
		{
			name: "stopped status is not testing",
			c:    Campaign{ABShare: 50, ABSubject: "B", Status: "stopped"},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Testing(); got != tc.want {
				t.Errorf("Testing() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCampaignPending(t *testing.T) {
	tests := []struct {
		c    Campaign
		want int
	}{
		{Campaign{Total: 100, Sent: 40, Errors: 5, Skipped: 10}, 45},
		{Campaign{Total: 100, Sent: 100, Errors: 0, Skipped: 0}, 0},
		{Campaign{Total: 0, Sent: 0, Errors: 0, Skipped: 0}, 0},
		{Campaign{Total: 50, Sent: 30, Errors: 20, Skipped: 0}, 0},
	}
	for _, tc := range tests {
		if got := tc.c.Pending(); got != tc.want {
			t.Errorf("Pending(%+v) = %d, want %d", tc.c, got, tc.want)
		}
	}
}

func TestCampaignPlanned(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	past := time.Now().Add(-1 * time.Hour)

	tests := []struct {
		name string
		c    Campaign
		want bool
	}{
		{
			name: "sending with future sendAt is planned",
			c:    Campaign{Status: "sending", SendAt: &future},
			want: true,
		},
		{
			name: "sending with past sendAt is not planned",
			c:    Campaign{Status: "sending", SendAt: &past},
			want: false,
		},
		{
			name: "sending with nil sendAt is not planned",
			c:    Campaign{Status: "sending", SendAt: nil},
			want: false,
		},
		{
			name: "draft with future sendAt is not planned",
			c:    Campaign{Status: "draft", SendAt: &future},
			want: false,
		},
		{
			name: "paused with future sendAt is not planned",
			c:    Campaign{Status: "paused", SendAt: &future},
			want: false,
		},
		{
			name: "done with future sendAt is not planned",
			c:    Campaign{Status: "done", SendAt: &future},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Planned(); got != tc.want {
				t.Errorf("Planned() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCampaignEditable(t *testing.T) {
	tests := []struct {
		status string
		want   bool
	}{
		{"draft", true},
		{"sending", false},
		{"paused", false},
		{"done", false},
		{"stopped", false},
		{"", false},
	}
	for _, tc := range tests {
		c := Campaign{Status: tc.status}
		if got := c.Editable(); got != tc.want {
			t.Errorf("Editable(Status=%q) = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestCampaignResendable(t *testing.T) {
	tests := []struct {
		name string
		c    Campaign
		want bool
	}{
		{
			name: "sent draft is not resendable",
			c:    Campaign{Status: "draft", Sent: 10},
			want: false,
		},
		{
			name: "done campaign with zero sent is not resendable",
			c:    Campaign{Status: "done", Sent: 0},
			want: false,
		},
		{
			name: "done campaign with sent > 0 is resendable",
			c:    Campaign{Status: "done", Sent: 50},
			want: true,
		},
		{
			name: "stopped campaign with sent > 0 is resendable",
			c:    Campaign{Status: "stopped", Sent: 15},
			want: true,
		},
		{
			name: "sending campaign with sent > 0 is resendable",
			c:    Campaign{Status: "sending", Sent: 1},
			want: true,
		},
		{
			name: "sending campaign with sent == 0 is not resendable",
			c:    Campaign{Status: "sending", Sent: 0},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.c.Resendable(); got != tc.want {
				t.Errorf("Resendable() = %v, want %v", got, tc.want)
			}
		})
	}
}
