package main

import (
	"strings"
	"testing"
)

func TestAsRows(t *testing.T) {
	// bare array
	rows := asRows([]any{
		map[string]any{"id": "a", "n": float64(1)},
		map[string]any{"id": "b", "n": float64(2)},
	})
	if len(rows) != 2 {
		t.Fatalf("bare array rows = %d", len(rows))
	}
	// data envelope
	rows = asRows(map[string]any{"data": []any{map[string]any{"id": "x"}}})
	if len(rows) != 1 || rows[0]["id"] != "x" {
		t.Fatalf("envelope rows = %#v", rows)
	}
	// nested objects are not tabular
	if asRows([]any{map[string]any{"nested": map[string]any{"a": 1}}}) != nil {
		t.Error("nested object should not be tabular")
	}
	// single flat object → one row
	rows = asRows(map[string]any{"id": "solo", "name": "x"})
	if len(rows) != 1 {
		t.Fatalf("single object rows = %d", len(rows))
	}
}

func TestColumnsStable(t *testing.T) {
	rows := []map[string]any{
		{"b": 1, "a": 2},
		{"a": 3, "c": 4},
	}
	cols := columns(rows)
	// first row's keys sorted, then new keys sorted
	if strings.Join(cols, ",") != "a,b,c" {
		t.Fatalf("columns = %v", cols)
	}
}

func TestScalarString(t *testing.T) {
	cases := map[any]string{
		nil:           "",
		"hi":          "hi",
		true:          "true",
		float64(42):   "42",
		float64(3.14): "3.14",
	}
	for in, want := range cases {
		if got := scalarString(in); got != want {
			t.Errorf("scalarString(%v) = %q, want %q", in, got, want)
		}
	}
}
