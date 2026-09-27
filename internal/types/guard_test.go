package types

import (
	"errors"
	"net/http"
	"testing"
)

func TestGuardDecisionValidate(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		loc     string
		wantErr bool
	}{
		{"redirect with explicit status", http.StatusSeeOther, "/login", false},
		{"redirect with zero status means 303", 0, "/login", false},
		{"permanent redirect", http.StatusMovedPermanently, "/login", false},
		{"bare refusal", http.StatusUnauthorized, "", false},
		{"forbidden", http.StatusForbidden, "", false},
		{"redirect without location", http.StatusSeeOther, "", true},
		{"success status with no location", http.StatusOK, "", true},
		{"no decision content at all", 0, "", true},
		{"redirect status paired with location but out of range", http.StatusMovedPermanently + 200, "/login", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &GuardDecision{Status: tc.status, Location: tc.loc}
			err := d.Validate()
			if tc.wantErr && !errors.Is(err, ErrInvalidGuardDecision) {
				t.Fatalf("Validate() = %v, want ErrInvalidGuardDecision", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
		})
	}
}
