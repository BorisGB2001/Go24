package apps

import (
	"strings"
	"testing"
)

// TestEntityLevel: the right to an entity is the highest of the codes the employee holds.
func TestEntityLevel(t *testing.T) {
	access := map[string]string{"AU": "R", "D5": "W", "U3": "X", "G7": "Z"}
	for _, c := range []struct {
		codes []string
		want  string
	}{{nil, ""}, {[]string{"U1"}, ""}, {[]string{"AU"}, "R"}, {[]string{"AU", "D5"}, "W"}, {[]string{"D5", "U3", "AU"}, "X"}, {[]string{"G7"}, ""}} {
		if got := Level(access, c.codes); got != c.want {
			t.Errorf("Level(%v) = %q, want %q", c.codes, got, c.want)
		}
	}
	if got, err := CleanAccess(map[string]string{" dr2 ": "w"}); err != nil || got["DR2"] != "W" {
		t.Errorf("CleanAccess: %v %v", got, err)
	}
	if _, err := CleanAccess(map[string]string{"AU": "Y"}); err == nil {
		t.Error("CleanAccess takes the level Y")
	}
}

// TestEntityWhere: the conditions become SQL with arguments; a property of a file and an unknown field are
// refused.
func TestEntityWhere(t *testing.T) {
	props := []EntityProp{{Code: "PRICE", Type: "N"}, {Code: "COLOR", Type: "S"}, {Code: "PHOTO", Type: "F"}}
	where, args, err := entityWhere(itemCols, props, []EntityCond{{Field: "P:price", Op: ">=", Vals: []string{"10"}},
		{Field: "NAME", Op: "%", Vals: []string{"50%"}}, {Field: "ACTIVE", Op: "=", Vals: []string{"Y"}}, {Field: "P:COLOR", Op: "=", Vals: []string{""}}}, []any{"e"})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"((x.props->>'PRICE'))::numeric >= $2::numeric", "x.name::text ILIKE '%' || $3 || '%'",
		"x.active = ANY($4::boolean[])", "((x.props->>'COLOR') IS NULL OR (x.props->>'COLOR')::text = '')"} {
		if !strings.Contains(where, part) {
			t.Errorf("no %q in %s", part, where)
		}
	}
	if len(args) != 4 || args[2] != `50\%` || args[3].([]string)[0] != "true" {
		t.Errorf("args: %#v", args)
	}
	for _, c := range []EntityCond{{Field: "P:PHOTO", Op: "=", Vals: []string{"1"}}, {Field: "PASSWORD", Op: "=", Vals: []string{"1"}},
		{Field: "P:PRICE", Op: "=", Vals: []string{"x"}}, {Field: "DATE_CREATE", Op: ">", Vals: []string{"вчера"}}} {
		if _, _, err := entityWhere(itemCols, props, []EntityCond{c}, nil); err == nil {
			t.Errorf("%v is taken", c)
		}
	}
	if by, err := entityOrder(itemCols, props, []EntitySort{{Field: "P:PRICE", Desc: true}}); err != nil || by != "((x.props->>'PRICE'))::numeric DESC NULLS LAST, x.id" {
		t.Errorf("entityOrder: %q %v", by, err)
	}
}
