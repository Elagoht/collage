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
		{"evil.attacker.io:6060", []string{"mybox.local", ""}, false},
		{"localhost.evil.attacker.io", nil, false},
		{"", []string{""}, false},
		// Reserved names (RFC 2606, RFC 6761): nobody can register them, so
		// nobody can point them at this machine to rebind.
		{"example.com", nil, true},
		{"www.example.com:443", nil, true},
		{"EXAMPLE.NET.", nil, true},
		{"api.example.org", nil, true},
		{"site.example", nil, true},
		{"app.test:6060", nil, true},
		{"x.invalid", nil, true},
		{"example", nil, false},
		{"test", nil, false},
		{"notexample.com", nil, false},
		{"example.com.evil.attacker.io2", nil, false},
		{"evil-example.com", nil, false},
		{"app.testing", nil, false},
		{":6060", nil, false},
	} {
		if got := Allowed(tc.host, tc.names...); got != tc.want {
			t.Errorf("Allowed(%q, %q) = %v, want %v", tc.host, tc.names, got, tc.want)
		}
	}
}

// COLLAGE_DEV_HOST is a comma-separated list; blanks and spaces are dropped.
func TestNames(t *testing.T) {
	got := Names(" app , mybox.local,,tenant-a.localhost ")
	want := []string{"app", "mybox.local", "tenant-a.localhost"}
	if len(got) != len(want) {
		t.Fatalf("Names = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names = %q, want %q", got, want)
		}
	}
	if Names("") != nil {
		t.Errorf("Names(\"\") = %q, want nil", Names(""))
	}
	if !Allowed("app:6060", Names("db, app")...) {
		t.Error("a name from the list was refused")
	}
}
