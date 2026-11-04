package crm

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFilterWhere(t *testing.T) {
	from := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 20, 10, 0, 0, 0, time.UTC)
	toPlusOne := to.AddDate(0, 0, 1)
	minAmt := 100.0
	maxAmt := 500.5

	tests := []struct {
		name     string
		filter   Filter
		entity   string
		alias    string
		initArgs []any
		wantW    string
		wantArgs []any
	}{
		{
			name:     "empty filter",
			filter:   Filter{},
			entity:   "deal",
			alias:    "d",
			initArgs: nil,
			wantW:    "",
			wantArgs: nil,
		},
		{
			name:     "stage and responsible with initial args",
			filter:   Filter{Stage: "stage-1", Responsible: "user-42"},
			entity:   "deal",
			alias:    "d",
			initArgs: []any{"existing1", "existing2"},
			wantW:    " AND d.stage_id::text = $3 AND d.responsible_id::text = $4",
			wantArgs: []any{"existing1", "existing2", "stage-1", "user-42"},
		},
		{
			name:     "closed semantic does not add sql argument",
			filter:   Filter{Semantic: "closed"},
			entity:   "deal",
			alias:    "d",
			initArgs: nil,
			wantW:    " AND st.semantic <> 'process'",
			wantArgs: nil,
		},
		{
			name:     "specific semantic adds argument",
			filter:   Filter{Semantic: "success"},
			entity:   "deal",
			alias:    "d",
			initArgs: nil,
			wantW:    " AND st.semantic = $1",
			wantArgs: []any{"success"},
		},
		{
			name:     "source and observer",
			filter:   Filter{Source: "src-1", Observer: "obs-9"},
			entity:   "lead",
			alias:    "l",
			initArgs: nil,
			wantW:    " AND l.source_id::text = $1 AND " + observerSQL("lead", "l", "$2"),
			wantArgs: []any{"src-1", "obs-9"},
		},
		{
			name:     "lead repeat 1",
			filter:   Filter{Repeat: "1"},
			entity:   "lead",
			alias:    "l",
			initArgs: nil,
			wantW:    " AND l.is_repeat",
			wantArgs: nil,
		},
		{
			name:     "lead repeat 0",
			filter:   Filter{Repeat: "0"},
			entity:   "lead",
			alias:    "l",
			initArgs: nil,
			wantW:    " AND NOT l.is_repeat",
			wantArgs: nil,
		},
		{
			name:     "deal repeat ignored",
			filter:   Filter{Repeat: "1"},
			entity:   "deal",
			alias:    "d",
			initArgs: nil,
			wantW:    "",
			wantArgs: nil,
		},
		{
			name:     "lead invalid repeat value ignored",
			filter:   Filter{Repeat: "yes"},
			entity:   "lead",
			alias:    "l",
			initArgs: nil,
			wantW:    "",
			wantArgs: nil,
		},
		{
			name:     "dates and amounts",
			filter:   Filter{From: &from, To: &to, AmountMin: &minAmt, AmountMax: &maxAmt},
			entity:   "deal",
			alias:    "d",
			initArgs: nil,
			wantW:    " AND d.created_at >= $1 AND d.created_at < $2 AND d.amount >= $3 AND d.amount <= $4",
			wantArgs: []any{from, toPlusOne, minAmt, maxAmt},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]any(nil), tt.initArgs...)
			gotW := tt.filter.where(tt.entity, tt.alias, &args)
			if gotW != tt.wantW {
				t.Errorf("where() = %q, want %q", gotW, tt.wantW)
			}
			if len(args) != len(tt.wantArgs) {
				t.Fatalf("args len = %d, want %d: got %v, want %v", len(args), len(tt.wantArgs), args, tt.wantArgs)
			}
			for i := range args {
				if !reflect.DeepEqual(args[i], tt.wantArgs[i]) {
					t.Errorf("args[%d] = %v (%T), want %v (%T)", i, args[i], args[i], tt.wantArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestFilterOrder(t *testing.T) {
	tests := []struct {
		name   string
		filter Filter
		alias  string
		want   string
	}{
		{
			name:   "default sort desc",
			filter: Filter{},
			alias:  "d",
			want:   " ORDER BY d.created_at DESC, d.created_at DESC",
		},
		{
			name:   "title sort asc",
			filter: Filter{Sort: "title", Asc: true},
			alias:  "d",
			want:   " ORDER BY d.title ASC, d.created_at DESC",
		},
		{
			name:   "amount sort desc",
			filter: Filter{Sort: "amount", Asc: false},
			alias:  "l",
			want:   " ORDER BY l.amount DESC, l.created_at DESC",
		},
		{
			name:   "stage sort asc uses fixed st.sort column",
			filter: Filter{Sort: "stage", Asc: true},
			alias:  "d",
			want:   " ORDER BY st.sort ASC, d.created_at DESC",
		},
		{
			name:   "responsible sort desc uses fixed r.name column",
			filter: Filter{Sort: "responsible", Asc: false},
			alias:  "d",
			want:   " ORDER BY r.name DESC, d.created_at DESC",
		},
		{
			name:   "unknown sort defaults to created",
			filter: Filter{Sort: "unknown_col", Asc: true},
			alias:  "x",
			want:   " ORDER BY x.created_at ASC, x.created_at DESC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.filter.order(tt.alias)
			if got != tt.want {
				t.Errorf("order(%q) = %q, want %q", tt.alias, got, tt.want)
			}
			if !strings.HasSuffix(got, ", "+tt.alias+".created_at DESC") {
				t.Errorf("order missing tie breaker: %s", got)
			}
		})
	}
}
