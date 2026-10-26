package suggest

import "testing"

func TestClosest(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		candidates []string
		want       string
	}{
		{"exact ignoring case wins over a nearer spelling", "TITLE", []string{"Titlf", "title"}, "title"},
		{"one substitution in a short name", "chek", []string{"check", "dev"}, "check"},
		{"a swap of two letters counts once", "Titel", []string{"Title", "Body"}, "Title"},
		{"one insertion in a short name", "ad", []string{"add", "del"}, "add"},
		{"two edits are too many under five letters", "pgx", []string{"page"}, ""},
		{"two edits are enough from five letters", "tempalte", []string{"template", "dir"}, "template"},
		{"three edits are too many from five letters", "frobnicate", []string{"new", "dev", "build"}, ""},
		{"ties go to the alphabetically first", "bat", []string{"cat", "bar"}, "bar"},
		{"nothing to choose from", "x", nil, ""},
		{"distance counts runes, not bytes", "çek", []string{"çok"}, "çok"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Closest(tt.input, tt.candidates); got != tt.want {
				t.Errorf("Closest(%q, %q) = %q, want %q", tt.input, tt.candidates, got, tt.want)
			}
		})
	}
}

func TestClosest_DoesNotReorderCandidates(t *testing.T) {
	candidates := []string{"b", "a"}
	Closest("c", candidates)
	if candidates[0] != "b" || candidates[1] != "a" {
		t.Errorf("candidates = %q, want them left as given", candidates)
	}
}
