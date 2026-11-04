package crm

import (
	"encoding/json"
	"testing"
	"time"
)

func TestRecycleItemPurgeAt_Unit(t *testing.T) {
	delTime := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	item := RecycleItem{DeletedAt: delTime}
	want := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	if got := item.PurgeAt(); !got.Equal(want) {
		t.Errorf("PurgeAt() = %v, want %v", got, want)
	}
}

func TestUnquote_Unit(t *testing.T) {
	// String JSON
	strRaw := json.RawMessage(`"hello world"`)
	s, ok := unquote(strRaw)
	if !ok || s != "hello world" {
		t.Errorf("unquote string = (%q, %v), want (\"hello world\", true)", s, ok)
	}

	// Non-string JSON (number)
	numRaw := json.RawMessage(`12345`)
	_, okNum := unquote(numRaw)
	if okNum {
		t.Errorf("unquote number returned ok=true, want false")
	}

	// Non-string JSON (object)
	objRaw := json.RawMessage(`{"key":"value"}`)
	_, okObj := unquote(objRaw)
	if okObj {
		t.Errorf("unquote object returned ok=true, want false")
	}
}

func TestIdent_Unit(t *testing.T) {
	if got := ident("table_name"); got != `"table_name"` {
		t.Errorf("ident(\"table_name\") = %q, want `\"table_name\"`", got)
	}
}
