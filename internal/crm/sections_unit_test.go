package crm

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

func TestSectionPaths_Unit(t *testing.T) {
	sections := []Section{
		{ID: "s1", Path: "Оборудование"},
		{ID: "s2", Path: "Оборудование / Принтеры"},
		{ID: "s3", Path: "Мебель"},
	}
	got := SectionPaths(sections)
	want := map[string]string{
		"s1": "Оборудование",
		"s2": "Оборудование / Принтеры",
		"s3": "Мебель",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("SectionPaths() = %+v, want %+v", got, want)
	}

	if gotEmpty := SectionPaths(nil); len(gotEmpty) != 0 {
		t.Errorf("SectionPaths(nil) = %+v, want empty", gotEmpty)
	}
}

type mockSQLErr struct {
	state string
}

func (m mockSQLErr) Error() string    { return "sql error " + m.state }
func (m mockSQLErr) SQLState() string { return m.state }

func TestSectionErr_Unit(t *testing.T) {
	// Foreign key violation 23503 -> ErrNotFound
	errFK := sectionErr(mockSQLErr{state: "23503"})
	if !errors.Is(errFK, ErrNotFound) {
		t.Errorf("sectionErr(23503) = %v, want ErrNotFound", errFK)
	}

	// Invalid text representation 22P02 -> ErrNotFound
	errInvalid := sectionErr(mockSQLErr{state: "22P02"})
	if !errors.Is(errInvalid, ErrNotFound) {
		t.Errorf("sectionErr(22P02) = %v, want ErrNotFound", errInvalid)
	}

	// Other error passed through
	otherErr := fmt.Errorf("other database error")
	if got := sectionErr(otherErr); got != otherErr {
		t.Errorf("sectionErr(otherErr) = %v, want %v", got, otherErr)
	}

	// Nil error
	if got := sectionErr(nil); got != nil {
		t.Errorf("sectionErr(nil) = %v, want nil", got)
	}
}
