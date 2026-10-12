package collage_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// A Duration is written as a string time.ParseDuration reads.
func TestDuration_UnmarshalString(t *testing.T) {
	for in, want := range map[string]time.Duration{
		`"10s"`:   10 * time.Second,
		`"1h30m"`: 90 * time.Minute,
		`"0s"`:    0,
		` "2ms" `: 2 * time.Millisecond,
	} {
		d := collage.Duration(time.Hour)
		if err := json.Unmarshal([]byte(in), &d); err != nil {
			t.Errorf("Unmarshal(%s) = %v", in, err)
			continue
		}
		if time.Duration(d) != want {
			t.Errorf("Unmarshal(%s) = %v, want %v", in, time.Duration(d), want)
		}
	}
}

// null leaves the value as it was: a section that says nothing keeps the default.
func TestDuration_UnmarshalNull(t *testing.T) {
	d := collage.Duration(time.Minute)
	if err := json.Unmarshal([]byte(`null`), &d); err != nil {
		t.Fatal(err)
	}
	if time.Duration(d) != time.Minute {
		t.Errorf("after null = %v, want the 1m it held", time.Duration(d))
	}
}

// A bare number is refused, naming it and the string to write instead: read as
// nanoseconds, "banTime": 3600 would be 3.6µs.
func TestDuration_UnmarshalNumberRefused(t *testing.T) {
	var cfg struct {
		BanTime collage.Duration `json:"banTime"`
	}
	err := json.Unmarshal([]byte(`{"banTime": 3600}`), &cfg)
	if err == nil {
		t.Fatalf("a number was accepted as %v", time.Duration(cfg.BanTime))
	}
	for _, want := range []string{"3600", `"10s"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// A string that is not a duration, and a value of another kind, are refused.
func TestDuration_UnmarshalInvalid(t *testing.T) {
	for _, in := range []string{`"10 parsecs"`, `""`, `true`, `["10s"]`, `{}`} {
		var d collage.Duration
		if err := json.Unmarshal([]byte(in), &d); err == nil {
			t.Errorf("Unmarshal(%s) accepted %v", in, time.Duration(d))
		}
	}
}

// A Duration is written as the string it is read from, and reads back the same.
func TestDuration_Marshal(t *testing.T) {
	out, err := json.Marshal(struct {
		D collage.Duration `json:"d"`
	}{collage.Duration(90 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"d":"1h30m0s"}` {
		t.Errorf("Marshal = %s", out)
	}
	var back struct {
		D collage.Duration `json:"d"`
	}
	if err := json.Unmarshal(out, &back); err != nil || time.Duration(back.D) != 90*time.Minute {
		t.Errorf("round trip = %v, %v", time.Duration(back.D), err)
	}
}
