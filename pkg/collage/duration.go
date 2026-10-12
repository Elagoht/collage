package collage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration that a plugin's options read from configuration
// as a string time.ParseDuration understands — "10s", "1h30m", "0s":
//
//	type Options struct {
//		BanTime collage.Duration `json:"banTime"`
//	}
//
// A bare number is refused rather than read as nanoseconds, the unit
// time.Duration's own decoding would give it: "banTime": 3600 meant an hour
// and would have been 3.6µs, silently. null leaves the value unchanged, so a
// default survives a section that says nothing. Convert with
// time.Duration(d) where a time.Duration is wanted.
type Duration time.Duration

// UnmarshalJSON reads a JSON string with time.ParseDuration; null leaves d as
// it was, and anything else — a number above all — is an error.
func (d *Duration) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if string(data) == "null" {
		return nil
	}
	if len(data) == 0 || data[0] != '"' {
		return fmt.Errorf("collage: duration %s: write it as a string such as \"10s\" or \"1h30m\"; a bare number has no unit", data)
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("collage: duration %s: %w", data, err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("collage: duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalJSON writes d as the string time.Duration's String gives, "1h30m0s",
// which UnmarshalJSON reads back.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

// String is time.Duration's String.
func (d Duration) String() string { return time.Duration(d).String() }
