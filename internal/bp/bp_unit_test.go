package bp

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func errKey(err error) string {
	var e *i18n.Error
	if errors.As(err, &e) {
		return e.Key
	}
	return ""
}

// TestCheckTemplate: template name and description constraints, entity types, auto flags, and steps validation.
func TestCheckTemplate(t *testing.T) {
	validStep := Step{Kind: "notify", To: "starter", Text: "Hello"}
	tests := []struct {
		name    string
		tpl     Template
		wantErr string
	}{
		{
			name: "valid template",
			tpl: Template{
				Name:   "Order Process",
				Steps:  []Step{validStep},
				Entity: "none",
			},
			wantErr: "",
		},
		{
			name: "empty name",
			tpl: Template{
				Name:  "   ",
				Steps: []Step{validStep},
			},
			wantErr: "err.bp_name",
		},
		{
			name: "name too long",
			tpl: Template{
				Name:  strings.Repeat("a", 201),
				Steps: []Step{validStep},
			},
			wantErr: "err.bp_name",
		},
		{
			name: "description too long",
			tpl: Template{
				Name:        "Valid Name",
				Description: strings.Repeat("d", 5001),
				Steps:       []Step{validStep},
			},
			wantErr: "err.bp_name",
		},
		{
			name: "invalid entity",
			tpl: Template{
				Name:   "Bad Entity",
				Entity: "unknown_entity",
				Steps:  []Step{validStep},
			},
			wantErr: "err.bad_form",
		},
		{
			name: "type_id set for non-deal",
			tpl: Template{
				Name:   "Bad Type",
				Entity: "lead",
				TypeID: "type-1",
				Steps:  []Step{validStep},
			},
			wantErr: "err.bad_form",
		},
		{
			name: "entity list without list_id",
			tpl: Template{
				Name:   "List Without ID",
				Entity: EntityList,
				Steps:  []Step{validStep},
			},
			wantErr: "err.bad_form",
		},
		{
			name: "list_id set for non-list",
			tpl: Template{
				Name:   "Non-List With ID",
				Entity: "deal",
				ListID: "list-1",
				Steps:  []Step{validStep},
			},
			wantErr: "err.bad_form",
		},
		{
			name: "no steps",
			tpl: Template{
				Name:   "No Steps",
				Entity: "none",
				Steps:  nil,
			},
			wantErr: "err.bp_no_steps",
		},
		{
			name: "invalid step kind",
			tpl: Template{
				Name:   "Bad Step",
				Entity: "none",
				Steps:  []Step{{Kind: "magic", Text: "Do magic"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "step text too long",
			tpl: Template{
				Name:   "Long Step Text",
				Entity: "none",
				Steps:  []Step{{Kind: "notify", Text: strings.Repeat("x", 1001)}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "step negative days",
			tpl: Template{
				Name:   "Negative Days",
				Entity: "none",
				Steps:  []Step{{Kind: "wait", Days: -1}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "step days over 365",
			tpl: Template{
				Name:   "Excessive Days",
				Entity: "none",
				Steps:  []Step{{Kind: "wait", Days: 366}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "task step without project",
			tpl: Template{
				Name:   "Task No Project",
				Entity: "none",
				Steps:  []Step{{Kind: "task", Text: "Task title"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "stage step on none entity",
			tpl: Template{
				Name:   "Stage None Entity",
				Entity: "none",
				Steps:  []Step{{Kind: "stage", Stage: "won"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "wait step with both days and field",
			tpl: Template{
				Name:   "Wait Both",
				Entity: "none",
				Fields: []Field{{Key: "until", Label: "Until", Kind: "date"}},
				Steps:  []Step{{Kind: "wait", Days: 5, Field: "until"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "wait step with neither days nor field",
			tpl: Template{
				Name:   "Wait Neither",
				Entity: "none",
				Steps:  []Step{{Kind: "wait"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "wait step with non-date field",
			tpl: Template{
				Name:   "Wait Non Date",
				Entity: "none",
				Fields: []Field{{Key: "title", Label: "Title", Kind: "string"}},
				Steps:  []Step{{Kind: "wait", Field: "title"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "webhook step with bad URL",
			tpl: Template{
				Name:   "Webhook Bad URL",
				Entity: "none",
				Steps:  []Step{{Kind: "webhook", URL: "not-a-url"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "absence step with invalid reason",
			tpl: Template{
				Name:   "Bad Absence Reason",
				Entity: "none",
				Fields: []Field{{Key: "from", Label: "From", Kind: "date"}, {Key: "to", Label: "To", Kind: "date"}},
				Steps:  []Step{{Kind: "absence", Absence: "invalid_reason", Start: "from", End: "to"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "event step with no users",
			tpl: Template{
				Name:   "Event No Users",
				Entity: "none",
				Fields: []Field{{Key: "at", Label: "At", Kind: "datetime"}},
				Steps:  []Step{{Kind: "event", Start: "at"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "request step with no fields",
			tpl: Template{
				Name:   "Request No Fields",
				Entity: "none",
				Steps:  []Step{{Kind: "request", Text: "Need info"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "non-request step with fields",
			tpl: Template{
				Name:   "Notify With Fields",
				Entity: "none",
				Steps:  []Step{{Kind: "notify", Text: "Hello", Fields: []Field{{Key: "f", Label: "F", Kind: "string"}}}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "request step with goto without reject",
			tpl: Template{
				Name:   "Request Goto No Reject",
				Entity: "none",
				Steps: []Step{
					{Kind: "request", Text: "Ask", Fields: []Field{{Key: "f", Label: "F", Kind: "string"}}, Goto: 2},
					validStep,
				},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "field:KEY referencing non-user field",
			tpl: Template{
				Name:   "To Non-User Field",
				Entity: "none",
				Fields: []Field{{Key: "amount", Label: "Amount", Kind: "number"}},
				Steps:  []Step{{Kind: "notify", To: "field:amount", Text: "Hi"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "pick step with const field",
			tpl: Template{
				Name:   "Pick Const Field",
				Entity: "none",
				Fields: []Field{{Key: "assignee", Label: "Assignee", Kind: "user", Scope: "const", Default: "123e4567-e89b-12d3-a456-426614174000"}},
				Steps:  []Step{{Kind: "pick", Users: []string{"user1"}, Field: "assignee"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "pick step with invalid mode",
			tpl: Template{
				Name:   "Pick Bad Mode",
				Entity: "none",
				Fields: []Field{{Key: "assignee", Label: "Assignee", Kind: "user", Scope: "var"}},
				Steps:  []Step{{Kind: "pick", Users: []string{"user1"}, Field: "assignee", Mode: "invalid_mode"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "set step targeting const field",
			tpl: Template{
				Name:   "Set Const Field",
				Entity: "none",
				Fields: []Field{{Key: "num", Label: "Num", Kind: "number", Scope: "const", Default: "42"}},
				Steps:  []Step{{Kind: "set", Field: "num", Value: "100"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "if step with unknown field",
			tpl: Template{
				Name:   "If Unknown Field",
				Entity: "none",
				Steps:  []Step{{Kind: "if", Field: "missing", Op: "eq", Value: "1"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "if step with unknown op",
			tpl: Template{
				Name:   "If Bad Op",
				Entity: "none",
				Fields: []Field{{Key: "status", Label: "Status", Kind: "string"}},
				Steps:  []Step{{Kind: "if", Field: "status", Op: "like", Value: "test"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "loop step jumping forward",
			tpl: Template{
				Name:   "Loop Forward",
				Entity: "none",
				Fields: []Field{{Key: "count", Label: "Count", Kind: "number"}},
				Steps: []Step{
					validStep,
					{Kind: "loop", Field: "count", Op: "gt", Value: "0", Goto: 3},
					validStep,
				},
			},
			wantErr: "err.bp_loop",
		},
		{
			name: "loop step jumping to self",
			tpl: Template{
				Name:   "Loop Self",
				Entity: "none",
				Fields: []Field{{Key: "count", Label: "Count", Kind: "number"}},
				Steps: []Step{
					validStep,
					{Kind: "loop", Field: "count", Op: "gt", Value: "0", Goto: 2},
				},
			},
			wantErr: "err.bp_loop",
		},
		{
			name: "goto step jumping to self",
			tpl: Template{
				Name:   "Goto Self",
				Entity: "none",
				Steps: []Step{
					{Kind: "goto", Goto: 1},
					validStep,
				},
			},
			wantErr: "err.bp_goto",
		},
		{
			name: "goto step jumping backwards",
			tpl: Template{
				Name:   "Goto Backward",
				Entity: "none",
				Steps: []Step{
					validStep,
					{Kind: "goto", Goto: 1},
				},
			},
			wantErr: "err.bp_goto",
		},
		{
			name: "goto step jumping beyond end",
			tpl: Template{
				Name:   "Goto Beyond End",
				Entity: "none",
				Steps: []Step{
					{Kind: "goto", Goto: 5},
					validStep,
				},
			},
			wantErr: "err.bp_goto",
		},
		{
			name: "goto step at the end with goto 0",
			tpl: Template{
				Name:   "Goto 0 At End",
				Entity: "none",
				Steps: []Step{
					validStep,
					{Kind: "goto", Goto: 0},
				},
			},
			wantErr: "err.bp_goto",
		},
		{
			name: "start step without template",
			tpl: Template{
				Name:   "Start No Template",
				Entity: "none",
				Steps:  []Step{{Kind: "start"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "delegate on unsupported step",
			tpl: Template{
				Name:   "Delegate On Notify",
				Entity: "none",
				Steps:  []Step{{Kind: "notify", Text: "Hello", Delegate: "all"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "invalid delegation type",
			tpl: Template{
				Name:   "Invalid Delegate",
				Entity: "none",
				Steps:  []Step{{Kind: "approve", Text: "Approve?", Delegate: "friends"}},
			},
			wantErr: "err.bp_step",
		},
		{
			name: "duplicate field keys in template",
			tpl: Template{
				Name:   "Duplicate Field Keys",
				Entity: "none",
				Fields: []Field{
					{Key: "city", Label: "City 1", Kind: "string"},
					{Key: "city", Label: "City 2", Kind: "string"},
				},
				Steps: []Step{validStep},
			},
			wantErr: "err.bp_field",
		},
		{
			name: "entity none resets auto flags",
			tpl: Template{
				Name:       "Auto Standalone",
				Entity:     "none",
				AutoCreate: true,
				AutoChange: true,
				Steps:      []Step{validStep},
			},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tpl := tt.tpl
			err := checkTemplate(&tpl, nil)
			gotKey := errKey(err)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && gotKey != tt.wantErr {
				t.Fatalf("errKey = %q (%v), want %q", gotKey, err, tt.wantErr)
			}
			if tt.name == "entity none resets auto flags" {
				if tpl.AutoCreate || tpl.AutoChange {
					t.Errorf("expected AutoCreate and AutoChange to be false for entity none")
				}
			}
		})
	}
}

// TestCheckField: field keys, kinds, select options and default values.
func TestCheckField(t *testing.T) {
	tests := []struct {
		name    string
		field   Field
		wantErr string
	}{
		{
			name:    "valid string field",
			field:   Field{Key: "title", Label: "Title", Kind: "string"},
			wantErr: "",
		},
		{
			name:    "empty key",
			field:   Field{Key: "", Label: "Title", Kind: "string"},
			wantErr: "err.bp_field",
		},
		{
			name:    "key with space",
			field:   Field{Key: "my title", Label: "Title", Kind: "string"},
			wantErr: "err.bp_field",
		},
		{
			name:    "key with colon",
			field:   Field{Key: "my:title", Label: "Title", Kind: "string"},
			wantErr: "err.bp_field",
		},
		{
			name:    "key with braces",
			field:   Field{Key: "my{title}", Label: "Title", Kind: "string"},
			wantErr: "err.bp_field",
		},
		{
			name:    "empty label",
			field:   Field{Key: "title", Label: "", Kind: "string"},
			wantErr: "err.bp_field",
		},
		{
			name:    "invalid kind",
			field:   Field{Key: "title", Label: "Title", Kind: "unknown"},
			wantErr: "err.bp_field",
		},
		{
			name:    "select without options",
			field:   Field{Key: "choice", Label: "Choice", Kind: "select", Options: nil},
			wantErr: "err.bp_field",
		},
		{
			name:    "select with empty option",
			field:   Field{Key: "choice", Label: "Choice", Kind: "select", Options: []string{"opt1", ""}},
			wantErr: "err.bp_field",
		},
		{
			name:    "select with option too long",
			field:   Field{Key: "choice", Label: "Choice", Kind: "select", Options: []string{strings.Repeat("o", 201)}},
			wantErr: "err.bp_field",
		},
		{
			name:    "non-select with options",
			field:   Field{Key: "title", Label: "Title", Kind: "string", Options: []string{"a"}},
			wantErr: "err.bp_field",
		},
		{
			name:    "number with invalid default",
			field:   Field{Key: "num", Label: "Num", Kind: "number", Default: "not_a_number"},
			wantErr: "err.bp_number",
		},
		{
			name:    "date with invalid default",
			field:   Field{Key: "dt", Label: "Date", Kind: "date", Default: "2026/11/02"},
			wantErr: "err.bp_number",
		},
		{
			name:    "select with default not in options",
			field:   Field{Key: "choice", Label: "Choice", Kind: "select", Options: []string{"yes", "no"}, Default: "maybe"},
			wantErr: "err.bp_number",
		},
		{
			name:    "user with invalid uuid default",
			field:   Field{Key: "mgr", Label: "Manager", Kind: "user", Default: "not-uuid"},
			wantErr: "err.bp_number",
		},
		{
			name:    "user with valid uuid default",
			field:   Field{Key: "mgr", Label: "Manager", Kind: "user", Default: "123e4567-e89b-12d3-a456-426614174000"},
			wantErr: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkField(tt.field)
			gotKey := errKey(err)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && gotKey != tt.wantErr {
				t.Fatalf("errKey = %q (%v), want %q", gotKey, err, tt.wantErr)
			}
		})
	}
}

// TestCheckParallel: branch count limits, step order, nested parallel/commands, jumps outside branches.
func TestCheckParallel(t *testing.T) {
	n := func(text string) Step { return Step{Kind: "notify", To: "starter", Text: text} }
	tests := []struct {
		name    string
		steps   []Step
		wantErr string
	}{
		{
			name: "valid 2 branches joining at end",
			steps: []Step{
				{Kind: "parallel", Branches: []int{3}, Goto: 0},
				n("branch 1"),
				n("branch 2"),
			},
			wantErr: "",
		},
		{
			name: "valid 2 branches joining before end",
			steps: []Step{
				{Kind: "parallel", Branches: []int{3}, Goto: 4},
				n("branch 1"),
				n("branch 2"),
				n("after join"),
			},
			wantErr: "",
		},
		{
			name: "non-parallel step with branches",
			steps: []Step{
				{Kind: "notify", Text: "Hi", Branches: []int{2}},
				n("step 2"),
			},
			wantErr: "err.bp_step",
		},
		{
			name: "parallel with no branches",
			steps: []Step{
				{Kind: "parallel", Branches: nil, Goto: 3},
				n("step 2"),
				n("step 3"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "parallel with branch starting too early",
			steps: []Step{
				{Kind: "parallel", Branches: []int{2}, Goto: 4},
				n("step 2"),
				n("step 3"),
				n("step 4"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "parallel with branch starting at or beyond join",
			steps: []Step{
				{Kind: "parallel", Branches: []int{4}, Goto: 4},
				n("step 2"),
				n("step 3"),
				n("step 4"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "nested parallel inside a branch",
			steps: []Step{
				{Kind: "parallel", Branches: []int{4}, Goto: 5},
				{Kind: "parallel", Branches: []int{3}, Goto: 0},
				n("inner branch 2"),
				n("branch 2"),
				n("after join"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "commands step inside a branch",
			steps: []Step{
				{Kind: "parallel", Branches: []int{3}, Goto: 4},
				{Kind: "commands", Commands: []Command{{Title: "Proceed", Goto: 0}}},
				n("branch 2"),
				n("after join"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "step inside branch jumps beyond branch end",
			steps: []Step{
				{Kind: "parallel", Branches: []int{3}, Goto: 4},
				{Kind: "goto", Goto: 4}, // branch 1 is [1, 2], Goto 4 jumps outside
				n("branch 2"),
				n("after join"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "loop inside branch jumps before branch start",
			steps: []Step{
				n("before parallel"),
				{Kind: "parallel", Branches: []int{4}, Goto: 5},
				n("branch 1 step 1"),
				{Kind: "loop", Field: "f", Op: "eq", Value: "1", Goto: 1}, // jumps to step before parallel
				n("branch 2"),
				n("after join"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "step before parallel jumps into a branch",
			steps: []Step{
				{Kind: "goto", Goto: 3}, // jumps into branch 1 of parallel
				{Kind: "parallel", Branches: []int{4}, Goto: 5},
				n("branch 1"),
				n("branch 2"),
				n("join"),
			},
			wantErr: "err.bp_parallel",
		},
		{
			name: "loop after join jumps into a branch",
			steps: []Step{
				{Kind: "parallel", Branches: []int{3}, Goto: 4},
				n("branch 1"),
				n("branch 2"),
				{Kind: "loop", Field: "f", Op: "eq", Value: "1", Goto: 2}, // jumps into branch 1
			},
			wantErr: "err.bp_parallel",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkParallel(tt.steps)
			gotKey := errKey(err)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != "" && gotKey != tt.wantErr {
				t.Fatalf("errKey = %q (%v), want %q", gotKey, err, tt.wantErr)
			}
		})
	}
}

// TestCheckCommands: commands step buttons, titles, jumps and loop constraints.
func TestCheckCommands(t *testing.T) {
	steps := []Step{
		{Kind: "notify", Text: "Start"},
		{
			Kind: "commands",
			Commands: []Command{
				{Title: "Proceed", Goto: 3},
				{Title: "Back", Goto: 1},
			},
		},
		{Kind: "notify", Text: "Done"},
	}

	if err := checkCommands(steps, 1); err != nil {
		t.Fatalf("unexpected error on valid commands: %v", err)
	}

	// Commands with duplicate title
	badSteps := []Step{
		{
			Kind: "commands",
			Commands: []Command{
				{Title: "OK", Goto: 0},
				{Title: "OK", Goto: 0},
			},
		},
	}
	if err := checkCommands(badSteps, 0); errKey(err) != "err.bp_step" {
		t.Errorf("duplicate titles: want err.bp_step, got %v", err)
	}

	// Command jumping to self (Goto == i+1)
	selfJump := []Step{
		{
			Kind: "commands",
			Commands: []Command{
				{Title: "Self", Goto: 1},
			},
		},
	}
	if err := checkCommands(selfJump, 0); errKey(err) != "err.bp_goto" {
		t.Errorf("self jump: want err.bp_goto, got %v", err)
	}

	// Command jumping backwards across a parallel step
	acrossParallel := []Step{
		{Kind: "notify", Text: "Before"},
		{Kind: "parallel", Branches: []int{3}, Goto: 4},
		{Kind: "notify", Text: "B1"},
		{Kind: "notify", Text: "B2"},
		{
			Kind: "commands",
			Commands: []Command{
				{Title: "Jump over parallel", Goto: 1},
			},
		},
	}
	if err := checkCommands(acrossParallel, 4); errKey(err) != "err.bp_goto" {
		t.Errorf("jump across parallel: want err.bp_goto, got %v", err)
	}
}

// TestCleanValue: value normalization across types and string length restrictions.
func TestCleanValue(t *testing.T) {
	// bool
	for _, v := range []string{"y", "YES", "on", "1", "True", "yEs"} {
		got, err := cleanValue(Field{Kind: "bool"}, v)
		if err != nil || got != "Y" {
			t.Errorf("cleanValue(bool, %q) = %q, %v, want Y", v, got, err)
		}
	}
	for _, v := range []string{"n", "NO", "off", "0", "false", "", "False"} {
		got, err := cleanValue(Field{Kind: "bool"}, v)
		if err != nil || got != "N" {
			t.Errorf("cleanValue(bool, %q) = %q, %v, want N", v, got, err)
		}
	}
	if _, err := cleanValue(Field{Kind: "bool"}, "unknown"); err == nil {
		t.Error("cleanValue(bool, unknown) expected error")
	}

	// number
	for _, v := range []string{"0", "123", "-45.67", "1,5", "  100  "} {
		if _, err := cleanValue(Field{Kind: "number"}, v); err != nil {
			t.Errorf("cleanValue(number, %q) unexpected error: %v", v, err)
		}
	}
	if _, err := cleanValue(Field{Kind: "number"}, "not-a-num"); err == nil {
		t.Error("cleanValue(number, not-a-num) expected error")
	}

	// date
	if got, err := cleanValue(Field{Kind: "date"}, "2026-11-02"); err != nil || got != "2026-11-02" {
		t.Errorf("cleanValue(date) = %q, %v", got, err)
	}
	if _, err := cleanValue(Field{Kind: "date"}, "02-11-2026"); err == nil {
		t.Error("cleanValue(date, bad) expected error")
	}

	// datetime
	if got, err := cleanValue(Field{Kind: "datetime"}, "2026-11-02 15:30"); err != nil || got != "2026-11-02T15:30" {
		t.Errorf("cleanValue(datetime with space) = %q, %v", got, err)
	}
	if got, err := cleanValue(Field{Kind: "datetime"}, "2026-11-02T15:30:45"); err != nil || got != "2026-11-02T15:30" {
		t.Errorf("cleanValue(datetime with seconds) = %q, %v", got, err)
	}

	// select
	selField := Field{Kind: "select", Options: []string{"A", "B", "C"}}
	if got, err := cleanValue(selField, "B"); err != nil || got != "B" {
		t.Errorf("cleanValue(select, B) = %q, %v", got, err)
	}
	if _, err := cleanValue(selField, "D"); err == nil {
		t.Error("cleanValue(select, D) expected error")
	}

	// user
	if _, err := cleanValue(Field{Kind: "user"}, "123e4567-e89b-12d3-a456-426614174000"); err != nil {
		t.Errorf("cleanValue(user, uuid) unexpected error: %v", err)
	}
	if _, err := cleanValue(Field{Kind: "user"}, "not-a-uuid"); err == nil {
		t.Error("cleanValue(user, not-a-uuid) expected error")
	}

	// length boundary > 10000 runes
	longStr := strings.Repeat("a", 10001)
	if _, err := cleanValue(Field{Kind: "string"}, longStr); errKey(err) != "err.bp_field" {
		t.Errorf("cleanValue(>10000) want err.bp_field, got %v", err)
	}
}

// TestCleanValues: defaults applied, strict checking for required fields.
func TestCleanValues(t *testing.T) {
	fields := []Field{
		{Key: "req", Label: "Required", Kind: "string", Required: true},
		{Key: "opt", Label: "Optional", Kind: "string", Default: "fallback"},
		{Key: "v", Label: "Var", Kind: "number", Scope: "var", Default: "10"},
	}

	// Strict mode with required field missing
	if _, err := cleanValues(fields, map[string]string{}, true); errKey(err) != "err.bp_required" {
		t.Errorf("cleanValues strict missing required: want err.bp_required, got %v", err)
	}

	// Non-strict mode with required field missing (e.g. autostarted process)
	clean, err := cleanValues(fields, map[string]string{}, false)
	if err != nil {
		t.Fatalf("cleanValues non-strict unexpected error: %v", err)
	}
	if clean["opt"] != "fallback" || clean["v"] != "10" {
		t.Errorf("cleanValues defaults mismatch: %+v", clean)
	}

	// Sent value for variable field is ignored: unasked fields always take Default
	clean, err = cleanValues(fields, map[string]string{"req": "value", "v": "99"}, true)
	if err != nil {
		t.Fatalf("cleanValues unexpected error: %v", err)
	}
	if clean["v"] != "10" {
		t.Errorf("cleanValues var field expected default '10', got %q", clean["v"])
	}
}

// TestIsUUID: valid and malformed UUID strings.
func TestIsUUID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"123e4567-e89b-12d3-a456-426614174000", true},
		{"123E4567-E89B-12D3-A456-426614174000", true},
		{"123e4567-e89b-12d3-a456-42661417400", false},   // too short
		{"123e4567-e89b-12d3-a456-4266141740000", false}, // too long
		{"123e4567e-89b-12d3-a456-426614174000", false},  // misplaced hyphen
		{"123e4567-e89b-12d3-a456-42661417400g", false},  // invalid hex char
		{"", false},
	}
	for _, tt := range tests {
		if got := isUUID(tt.id); got != tt.want {
			t.Errorf("isUUID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

// TestAllFieldsAndUserInfoLabel: field aggregation and localized label resolution.
func TestAllFieldsAndUserInfoLabel(t *testing.T) {
	form := []Field{{Key: "f1", Label: "F1", Kind: "string"}}
	steps := []Step{
		{Kind: "request", Fields: []Field{{Key: "req_f", Label: "ReqF", Kind: "number"}}},
		{Kind: "userinfo"},
		{Kind: "crmread"},
		{Kind: "diskread"},
	}

	all := AllFields(form, steps)
	keys := make(map[string]bool)
	for _, f := range all {
		keys[f.Key] = true
	}

	if !keys["f1"] || !keys["req_f"] || !keys["user_name"] || !keys["crm_amount"] || !keys["disk_size"] {
		t.Errorf("AllFields missing expected keys: %+v", all)
	}

	// UserInfoLabel
	if UserInfoLabel("user_email") != "bp.userinfo.email" {
		t.Errorf("UserInfoLabel(user_email) = %q", UserInfoLabel("user_email"))
	}
	if UserInfoLabel("crm_amount") != "bp.crminfo.amount" {
		t.Errorf("UserInfoLabel(crm_amount) = %q", UserInfoLabel("crm_amount"))
	}
	if UserInfoLabel("disk_by") != "bp.diskinfo.by" {
		t.Errorf("UserInfoLabel(disk_by) = %q", UserInfoLabel("disk_by"))
	}
	if UserInfoLabel("other_key") != "" {
		t.Errorf("UserInfoLabel(other_key) = %q, want empty", UserInfoLabel("other_key"))
	}
}

// TestInstanceCurrentAndState: state-machine queries for running and stopped processes.
func TestInstanceCurrentAndState(t *testing.T) {
	steps := []Step{
		{Kind: "notify", Text: "Step 1"},
		{Kind: "notify", Text: "Step 2"},
		{Kind: "notify", Text: "Step 3"},
	}

	// Process running on Step 1
	inst := Instance{
		Status: "running",
		Step:   1,
		Steps:  steps,
	}

	if inst.Current(0) || !inst.Current(1) || inst.Current(2) {
		t.Errorf("Current() mismatch on linear execution")
	}
	if inst.State(0) != "done" || inst.State(1) != "now" || inst.State(2) != "" {
		t.Errorf("State() mismatch on linear execution: 0:%s 1:%s 2:%s", inst.State(0), inst.State(1), inst.State(2))
	}

	// Process not running
	inst.Status = "done"
	if inst.Current(1) {
		t.Error("Current() must be false when status is done")
	}
	if inst.State(1) != "" {
		t.Errorf("State(1) = %q, want empty when done", inst.State(1))
	}

	// Parallel process with branches
	parallelSteps := []Step{
		{Kind: "parallel", Branches: []int{3}, Goto: 5}, // step 0: branch 1 is [1, 2), branch 2 is [2, 4)
		{Kind: "notify", Text: "B1 S1"},                 // step 1 (index 1)
		{Kind: "notify", Text: "B2 S1"},                 // step 2 (index 2)
		{Kind: "notify", Text: "B2 S2"},                 // step 3 (index 3)
		{Kind: "notify", Text: "Join"},                  // step 5 (index 4)
	}
	pInst := Instance{
		Status:   "running",
		Step:     0,
		Steps:    parallelSteps,
		Branches: []int{1, 3}, // branch 0 is at step 1, branch 1 is at step 3
	}

	if !pInst.Current(1) || !pInst.Current(3) {
		t.Errorf("parallel branches Current() mismatch: 1:%v 3:%v", pInst.Current(1), pInst.Current(3))
	}
	if pInst.State(2) != "done" {
		t.Errorf("step 2 in branch 2 should be done when branch is at step 3, got %q", pInst.State(2))
	}
}

// TestRanges: parallel branch range partitioning.
func TestRanges(t *testing.T) {
	steps := []Step{
		{Kind: "parallel", Branches: []int{3, 5}, Goto: 7},
		{Kind: "notify"}, // 1
		{Kind: "notify"}, // 2
		{Kind: "notify"}, // 3
		{Kind: "notify"}, // 4
		{Kind: "notify"}, // 5
		{Kind: "notify"}, // 6
		{Kind: "notify"}, // 7 (join)
	}

	ranges := Ranges(steps, 0)
	if len(ranges) != 3 {
		t.Fatalf("expected 3 ranges, got %d", len(ranges))
	}
	expected := [][2]int{{1, 2}, {2, 4}, {4, 6}}
	for i, r := range ranges {
		if r != expected[i] {
			t.Errorf("range %d = %v, want %v", i, r, expected[i])
		}
	}
}

// TestFieldShownAndAsked: Field helper methods.
func TestFieldShownAndAsked(t *testing.T) {
	dt := Field{Kind: "datetime"}
	if got := dt.Shown("2026-11-02T15:30"); got != "2026-11-02 15:30" {
		t.Errorf("Shown(datetime) = %q, want '2026-11-02 15:30'", got)
	}
	str := Field{Kind: "string"}
	if got := str.Shown("hello"); got != "hello" {
		t.Errorf("Shown(string) = %q, want 'hello'", got)
	}

	fAsked := Field{Scope: ""}
	if !fAsked.Asked() {
		t.Error("Scope '' should be Asked")
	}
	fVar := Field{Scope: "var"}
	fConst := Field{Scope: "const"}
	if fVar.Asked() || fConst.Asked() {
		t.Error("Scope 'var'/'const' should not be Asked")
	}
}

// TestStepPeople: people deduplication and collection from step To and Users.
func TestStepPeople(t *testing.T) {
	st := Step{To: "starter", Users: []string{"user1", "", "user2"}}
	people := st.People()
	expected := []string{"starter", "user1", "user2"}
	if len(people) != len(expected) {
		t.Fatalf("People() len = %d, want %d", len(people), len(expected))
	}
	for i, p := range people {
		if p != expected[i] {
			t.Errorf("People()[%d] = %q, want %q", i, p, expected[i])
		}
	}
}
