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
// and would have been 3.6µs, silently. A negative duration is refused too. null
// leaves a Duration field as it was, so a default survives a section that says
// nothing; a *Duration field set to null becomes nil, as encoding/json makes
// any pointer. Convert with time.Duration(d) where a time.Duration is wanted.
//
// Its errors carry no prefix of their own: PluginConfig puts the plugin and the
// key in front of them.
type Duration time.Duration

// UnmarshalJSON reads a JSON string with time.ParseDuration; null leaves d as
// it was, and anything else — a number above all, or a negative duration — is
// an error.
func (d *Duration) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if string(data) == "null" {
		return nil
	}
	if len(data) == 0 || data[0] != '"' {
		return fmt.Errorf("write a duration as a string such as \"10s\", not %s", data)
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("duration %s: %w", data, err)
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration such as \"10s\": %w", s, err)
	}
	if parsed < 0 {
		return fmt.Errorf("duration %q is negative", s)
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
