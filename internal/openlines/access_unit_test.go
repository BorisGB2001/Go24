package openlines

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestWhoCond(t *testing.T) {
	t.Run("all true returns literal true without args", func(t *testing.T) {
		called := false
		arg := func(v any) string {
			called = true
			return "$1"
		}
		got := (Who{All: true}).cond(arg)
		if got != "true" {
			t.Errorf("cond = %q; want 'true'", got)
		}
		if called {
			t.Error("arg function must not be called when All is true")
		}
	})

	t.Run("empty user id returns literal false without args", func(t *testing.T) {
		called := false
		arg := func(v any) string {
			called = true
			return "$1"
		}
		got := (Who{UserID: ""}).cond(arg)
		if got != "false" {
			t.Errorf("cond = %q; want 'false'", got)
		}
		if called {
			t.Error("arg function must not be called when UserID is empty")
		}
	})

	t.Run("user id with nil team passes empty slice", func(t *testing.T) {
		var passed []any
		arg := func(v any) string {
			passed = append(passed, v)
			return "$" + strconv.Itoa(len(passed))
		}

		who := Who{UserID: "user-123", Team: nil}
		got := who.cond(arg)

		if len(passed) != 2 {
			t.Fatalf("expected 2 arguments passed to arg func, got %d: %v", len(passed), passed)
		}
		if passed[0] != "user-123" {
			t.Errorf("arg[0] = %v; want 'user-123'", passed[0])
		}
		// nonNil(nil) must be empty slice []string{}
		if !reflect.DeepEqual(passed[1], []string{}) {
			t.Errorf("arg[1] = %v; want empty []string{}", passed[1])
		}

		if !strings.Contains(got, "s.operator_id::text = $1") {
			t.Errorf("cond missing operator_id check: %s", got)
		}
		if !strings.Contains(got, "ANY($2::text[])") {
			t.Errorf("cond missing team ANY check: %s", got)
		}
	})

	t.Run("user id with populated team", func(t *testing.T) {
		var passed []any
		arg := func(v any) string {
			passed = append(passed, v)
			return "$" + strconv.Itoa(len(passed))
		}

		who := Who{UserID: "manager-1", Team: []string{"op-1", "op-2"}}
		got := who.cond(arg)

		if len(passed) != 2 {
			t.Fatalf("expected 2 args, got %d", len(passed))
		}
		if passed[0] != "manager-1" {
			t.Errorf("arg[0] = %v; want manager-1", passed[0])
		}
		wantTeam := []string{"op-1", "op-2"}
		if !reflect.DeepEqual(passed[1], wantTeam) {
			t.Errorf("arg[1] = %v; want %v", passed[1], wantTeam)
		}
		if !strings.Contains(got, "cardinality(w.operators) = 0 AND w.created_by::text = $1") {
			t.Errorf("cond missing created_by fallback: %s", got)
		}
	})
}
