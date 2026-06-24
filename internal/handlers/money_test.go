package handlers

import "testing"

func TestParseCurrency(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"R$ 54,20", 5420},
		{"54,20", 5420},
		{"1.234,56", 123456},
		{"1234.56", 123456},
		{"1,234.56", 123456},
		{"R$1.234,56", 123456},
		{"  260000,00  ", 26000000},
		{"100", 10000},
		{"100,5", 10050},
		{"0,99", 99},
	}

	for _, tc := range cases {
		got, err := parseCurrency(tc.in)
		if err != nil {
			t.Fatalf("parseCurrency(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("parseCurrency(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
