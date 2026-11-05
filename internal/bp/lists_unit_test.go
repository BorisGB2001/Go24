package bp

import (
	"testing"
)

// TestListField: field key lookup by "name", key, or field label (case-insensitive).
func TestListField(t *testing.T) {
	l := List{
		Name: "Products",
		Fields: []Field{
			{Key: "sku", Label: "Артикул", Kind: "string"},
			{Key: "price", Label: "Цена", Kind: "number"},
		},
	}

	tests := []struct {
		input   string
		wantKey string
		wantOK  bool
	}{
		{"name", "name", true},
		{"NAME", "name", true},
		{"  Name  ", "name", true},
		{"sku", "sku", true},
		{"SKU", "", false}, // keys are exact match
		{"Артикул", "sku", true},
		{"артикул", "sku", true}, // labels are case-insensitive
		{"Цена", "price", true},
		{"unknown", "", false},
	}

	for _, tt := range tests {
		gotKey, gotOK := listField(l, tt.input)
		if gotOK != tt.wantOK || gotKey != tt.wantKey {
			t.Errorf("listField(%q) = (%q, %v), want (%q, %v)", tt.input, gotKey, gotOK, tt.wantKey, tt.wantOK)
		}
	}
}

// TestListReadFields: conversion of list fields into process fields with el_ prefix.
func TestListReadFields(t *testing.T) {
	l := List{
		Name: "Warehouse",
		Fields: []Field{
			{Key: "code", Label: "Code", Kind: "string"},
			{Key: "count", Label: "Count", Kind: "number"},
			{Key: "status", Label: "Status", Kind: "select", Options: []string{"new", "done"}},
		},
	}

	fields := ListReadFields(l, "Element Title")
	if len(fields) != 4 {
		t.Fatalf("ListReadFields() len = %d, want 4", len(fields))
	}

	if fields[0].Key != "el_name" || fields[0].Label != "Warehouse: Element Title" || fields[0].Kind != "string" {
		t.Errorf("el_name mismatch: %+v", fields[0])
	}
	if fields[1].Key != "el_code" || fields[1].Label != "Warehouse: Code" || fields[1].Kind != "string" {
		t.Errorf("el_code mismatch: %+v", fields[1])
	}
	if fields[2].Key != "el_count" || fields[2].Label != "Warehouse: Count" || fields[2].Kind != "number" {
		t.Errorf("el_count mismatch: %+v", fields[2])
	}
	if fields[3].Key != "el_status" || len(fields[3].Options) != 2 {
		t.Errorf("el_status mismatch: %+v", fields[3])
	}
}

// TestListColumnFields: filtering list fields by visible table columns while maintaining field order.
func TestListColumnFields(t *testing.T) {
	f1 := Field{Key: "f1", Label: "Field 1"}
	f2 := Field{Key: "f2", Label: "Field 2"}
	f3 := Field{Key: "f3", Label: "Field 3"}

	l := List{
		Fields:  []Field{f1, f2, f3},
		Columns: []string{"f3", "f1", "non_existent"},
	}

	cols := l.ColumnFields()
	if len(cols) != 2 {
		t.Fatalf("ColumnFields() len = %d, want 2", len(cols))
	}
	// Preserves order of Fields: f1, then f3
	if cols[0].Key != "f1" || cols[1].Key != "f3" {
		t.Errorf("ColumnFields() order mismatch: %+v", cols)
	}
}

