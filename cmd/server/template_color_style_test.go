package main

import (
	"html/template"
	"testing"
)

func TestSafeColorStyleAllowsKnownColorFormats(t *testing.T) {
	tests := []struct {
		name     string
		property string
		value    string
		want     template.CSS
	}{
		{
			name:     "rgb color",
			property: "color",
			value:    "rgb(239 68 68)",
			want:     template.CSS("color:rgb(239 68 68)"),
		},
		{
			name:     "hex background",
			property: "background-color",
			value:    "#22c55e",
			want:     template.CSS("background-color:#22c55e"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeColorStyle(tc.property, tc.value); got != tc.want {
				t.Fatalf("safeColorStyle(%q, %q) = %q, want %q", tc.property, tc.value, got, tc.want)
			}
		})
	}
}

func TestSafeColorStyleRejectsUnsafeValues(t *testing.T) {
	tests := []struct {
		name     string
		property string
		value    string
	}{
		{name: "unsupported property", property: "border-color", value: "#fff"},
		{name: "expression", property: "color", value: "expression(alert(1))"},
		{name: "url injection", property: "background-color", value: "url(javascript:alert(1))"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeColorStyle(tc.property, tc.value); got != "" {
				t.Fatalf("safeColorStyle(%q, %q) = %q, want empty", tc.property, tc.value, got)
			}
		})
	}
}
