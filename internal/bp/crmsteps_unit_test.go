package bp

import (
	"context"
	"testing"
)

// TestStepCard: extraction of CRM card ID from direct value, URL link, process entity or created_card.
func TestStepCard(t *testing.T) {
	s := &Store{}
	ctx := context.Background()

	tests := []struct {
		name    string
		inst    Instance
		step    Step
		entity  string
		wantID  string
		wantErr string
	}{
		{
			name:   "direct ID value",
			inst:   Instance{Entity: "none"},
			step:   Step{Value: "deal-100"},
			entity: "deal",
			wantID: "deal-100",
		},
		{
			name:   "URL with matching entity kind",
			inst:   Instance{Entity: "none"},
			step:   Step{Value: "https://example.com/crm/deals/deal-200"},
			entity: "deal",
			wantID: "deal-200",
		},
		{
			name:   "relative URL with matching entity kind",
			inst:   Instance{Entity: "none"},
			step:   Step{Value: "/crm/leads/lead-300"},
			entity: "lead",
			wantID: "lead-300",
		},
		{
			name:    "URL with mismatched entity kind",
			inst:    Instance{Entity: "none"},
			step:    Step{Value: "/crm/deals/deal-200"},
			entity:  "lead", // expected lead but URL is for deals
			wantErr: "err.bp_crm_card",
		},
		{
			name: "fallback to created_card when value is empty",
			inst: Instance{
				Entity: "none",
				Values: map[string]string{"created_card": "/crm/deals/deal-created"},
			},
			step:   Step{Value: ""},
			entity: "deal",
			wantID: "deal-created",
		},
		{
			name:    "empty value without matching process entity or created_card",
			inst:    Instance{Entity: "none"},
			step:    Step{Value: ""},
			entity:  "deal",
			wantErr: "err.bp_crm_card",
		},
		{
			name:    "invalid characters in extracted ID",
			inst:    Instance{Entity: "none"},
			step:    Step{Value: "invalid/id with spaces"},
			entity:  "deal",
			wantErr: "err.bp_crm_card",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.stepCard(ctx, tt.inst, tt.step, tt.entity)
			gotKey := errKey(err)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if got != tt.wantID {
					t.Fatalf("stepCard() = %q, want %q", got, tt.wantID)
				}
			} else {
				if gotKey != tt.wantErr {
					t.Fatalf("errKey = %q (%v), want %q", gotKey, err, tt.wantErr)
				}
			}
		})
	}
}

