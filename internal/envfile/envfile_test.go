package envfile

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse_Format(t *testing.T) {
	content := `# a comment

PORT=3000
export HOST=0.0.0.0
  SPACED = around  
DOUBLE="a # not a comment"
SINGLE='it''s'
EMPTY=
TRAILING=3000 # a comment
QUOTED_THEN_COMMENT="x" # fine
EQUALS=a=b
`
	// SINGLE is 'it' followed by 's' — text after the closing quote.
	if _, err := Parse(".env", content); err == nil || !strings.Contains(err.Error(), ".env:7") {
		t.Fatalf("Parse() error = %v, want one naming .env:7", err)
	}

	content = strings.Replace(content, `SINGLE='it''s'`, `SINGLE='it is'`, 1)
	pairs, err := Parse(".env", content)
	if err != nil {
		t.Fatalf("Parse() = %v, want nil", err)
	}
	want := [][2]string{
		{"PORT", "3000"},
		{"HOST", "0.0.0.0"},
		{"SPACED", "around"},
		{"DOUBLE", "a # not a comment"},
		{"SINGLE", "it is"},
		{"EMPTY", ""},
		{"TRAILING", "3000"},
		{"QUOTED_THEN_COMMENT", "x"},
		{"EQUALS", "a=b"},
	}
	if !reflect.DeepEqual(pairs, want) {
		t.Errorf("pairs = %q\nwant  %q", pairs, want)
	}
}

// A line that is not a setting is an error naming the file and the line, never a
// setting silently left out.
func TestParse_MalformedLines(t *testing.T) {
	for _, line := range []string{
		"JUST_A_WORD",
		"1PORT=3000",
		"MY-KEY=x",
		"=value",
		`OPEN="never closed`,
	} {
		t.Run(line, func(t *testing.T) {
			_, err := Parse(".env.development", "OK=1\n"+line+"\n")
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Parse() error = %v, want ErrMalformed", err)
			}
			if !strings.Contains(err.Error(), ".env.development:2") {
				t.Errorf("error = %q, want it to name .env.development:2", err)
			}
		})
	}
}
