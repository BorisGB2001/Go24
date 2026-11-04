package crm

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestCleanProps_Unit(t *testing.T) {
	props := []Prop{
		{ID: "num1", Name: "Вес", Kind: PropNumber},
		{ID: "list1", Name: "Цвет", Kind: PropList, Options: []string{"Красный", "Синий"}},
		{ID: "bool1", Name: "В наличии", Kind: PropBool},
		{ID: "str1", Name: "Заметка", Kind: PropString},
	}

	// 1. Success case
	in := map[string]string{
		"num1":    "  1 234,50  ",
		"list1":   "Синий",
		"bool1":   "any-truthy-string",
		"str1":    "Текст заметки",
		"unknown": "should be ignored",
		"empty":   "   ",
	}
	got, err := cleanProps(props, in)
	if err != nil {
		t.Fatalf("cleanProps() unexpected error: %v", err)
	}
	want := map[string]string{
		"num1":  "1234.5",
		"list1": "Синий",
		"bool1": "1",
		"str1":  "Текст заметки",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanProps() = %+v, want %+v", got, want)
	}

	// 2. Bool "0" is dropped
	gotBool0, err := cleanProps(props, map[string]string{"bool1": "0"})
	if err != nil || len(gotBool0) != 0 {
		t.Errorf("expected bool \"0\" to be dropped, got %+v (err: %v)", gotBool0, err)
	}

	// 3. Invalid PropNumber returns err.crm_prop_number
	_, err = cleanProps(props, map[string]string{"num1": "not-a-number"})
	if err == nil {
		t.Fatal("expected error on invalid number prop, got nil")
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.crm_prop_number" {
		t.Errorf("expected err.crm_prop_number, got %v", err)
	}

	// 4. Invalid PropList returns err.crm_prop_options
	_, err = cleanProps(props, map[string]string{"list1": "Зеленый"})
	if err == nil {
		t.Fatal("expected error on invalid list prop option, got nil")
	}
	if !errors.As(err, &ie) || ie.Key != "err.crm_prop_options" {
		t.Errorf("expected err.crm_prop_options, got %v", err)
	}

	// 5. String prop exceeding 1000 runes returns err.crm_field
	_, err = cleanProps(props, map[string]string{"str1": strings.Repeat("a", 1005)})
	if err == nil {
		t.Fatal("expected error on long string prop, got nil")
	}
	if !errors.As(err, &ie) || ie.Key != "err.crm_field" {
		t.Errorf("expected err.crm_field, got %v", err)
	}
}

func TestCleanBarcodes_Unit(t *testing.T) {
	// 1. Success case: deduplication and trimming
	in := []string{"  12345  ", "67890", "", "12345", "99999"}
	got, err := cleanBarcodes(in)
	if err != nil {
		t.Fatalf("cleanBarcodes() unexpected error: %v", err)
	}
	want := []string{"12345", "67890", "99999"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cleanBarcodes() = %v, want %v", got, want)
	}

	// 2. Barcode with space in middle returns err.crm_barcode
	_, err = cleanBarcodes([]string{"123 456"})
	if err == nil {
		t.Fatal("expected error on barcode with space, got nil")
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.crm_barcode" {
		t.Errorf("expected err.crm_barcode, got %v", err)
	}

	// 3. Barcode with tab in middle returns err.crm_barcode
	_, err = cleanBarcodes([]string{"123\t456"})
	if !errors.As(err, &ie) || ie.Key != "err.crm_barcode" {
		t.Errorf("expected err.crm_barcode on tab, got %v", err)
	}

	// 4. Barcode longer than 64 characters returns err.crm_barcode
	_, err = cleanBarcodes([]string{strings.Repeat("1", 65)})
	if !errors.As(err, &ie) || ie.Key != "err.crm_barcode" {
		t.Errorf("expected err.crm_barcode on >64 chars, got %v", err)
	}

	// 5. More than 50 unique barcodes returns err.crm_rows
	many := make([]string, 51)
	for i := range many {
		many[i] = string(rune('A'+i/26)) + string(rune('a'+i%26)) + "123456"
	}
	_, err = cleanBarcodes(many)
	if err == nil {
		t.Fatal("expected error on >50 barcodes, got nil")
	}
	if !errors.As(err, &ie) || ie.Key != "err.crm_rows" {
		t.Errorf("expected err.crm_rows, got %v", err)
	}
}
