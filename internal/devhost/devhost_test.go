package devhost

import "testing"

func TestAllowed(t *testing.T) {
	for _, tc := range []struct {
		host  string
		names []string
		want  bool
	}{
		{"localhost", nil, true},
		{"localhost:6060", nil, true},
		{"LOCALHOST.:6060", nil, true},
		{"app.localhost:6060", nil, true},
		{"127.0.0.1:6060", nil, true},
		{"[::1]:6060", nil, true},
		{"::1", nil, true},
		{"192.168.1.20:6060", nil, true},
		{"mybox.local:6060", []string{"mybox.local"}, true},
		{"MyBox.Local.:6060", []string{"mybox.local:6060"}, true},
		{"mybox.local:6060", nil, false},
		{"evil.example:6060", []string{"mybox.local", ""}, false},
		{"localhost.evil.example", nil, false},
		{"", []string{""}, false},
		{":6060", nil, false},
	} {
		if got := Allowed(tc.host, tc.names...); got != tc.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tc.host, tc.names, got, tc.want)
		}
	}
}
