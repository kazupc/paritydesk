package ids

import (
	"strings"
	"testing"
)

func TestParseSession(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"local_00000000-0000-4000-8000-000000000001", true},
		{"local_" + strings.Repeat("-", 36), false},
		{"local_" + strings.Repeat("0", 36), false},
		{"local_00000000-0000-4000-8000-00000000000G", false},
		{"local_00000000-0000-4000-8000-000000000001x", false},
		{"00000000-0000-4000-8000-000000000001", false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			_, err := ParseSession(tc.in)
			if (err == nil) != tc.want {
				t.Fatalf("ParseSession(%q) err = %v, want ok %v", tc.in, err, tc.want)
			}
		})
	}
}
