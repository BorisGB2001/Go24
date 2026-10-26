package kb

import (
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	got := Diff("a\nb\nc", "a\nc\nd")
	want := []DiffLine{{"=", "a"}, {"-", "b"}, {"=", "c"}, {"+", "d"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("diff = %v", got)
	}
	if got := Diff("", "x"); !reflect.DeepEqual(got, []DiffLine{{"-", ""}, {"+", "x"}}) {
		t.Fatalf("diff of empty = %v", got)
	}
}
