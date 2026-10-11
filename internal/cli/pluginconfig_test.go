package cli

import "testing"

func TestAddConfigSection(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"absent file", "", "{\n  \"a/b\": {}\n}\n"},
		{"empty object", "{}", "{\n  \"a/b\": {}\n}"},
		{"two spaces", "{\n  \"x/y\": {\"k\": 1}\n}\n", "{\n  \"x/y\": {\"k\": 1},\n  \"a/b\": {}\n}\n"},
		{"tabs", "{\n\t\"x/y\": {}\n}", "{\n\t\"x/y\": {},\n\t\"a/b\": {}\n}"},
		{"crlf", "{\r\n  \"$schema\": \"s\"\r\n}\r\n", "{\r\n  \"$schema\": \"s\",\r\n  \"a/b\": {}\r\n}\r\n"},
		{"one line", `{"x/y": {}}`, `{"x/y": {}, "a/b": {}}`},
	} {
		var src []byte
		if tc.in != "" {
			src = []byte(tc.in)
		}
		out, added, err := addConfigSection(src, "a/b")
		if err != nil || !added || string(out) != tc.want {
			t.Errorf("%s: %q, %v, %v; want %q", tc.name, out, added, err, tc.want)
		}
	}
	keep := []byte("{\n  \"a/b\": {\"k\": 2}\n}\n")
	if out, added, err := addConfigSection(keep, "a/b"); err != nil || added || string(out) != string(keep) {
		t.Errorf("existing section touched: %q %v %v", out, added, err)
	}
	for _, bad := range []string{"[]", "1", "{", `{"a":1} trailing`} {
		if _, _, err := addConfigSection([]byte(bad), "a/b"); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestRemoveConfigSection(t *testing.T) {
	for _, tc := range []struct{ name, in, want, removed string }{
		{"last", "{\n  \"x/y\": {},\n  \"a/b\": {\"k\": 1}\n}\n", "{\n  \"x/y\": {}\n}\n", `{"k": 1}`},
		{"first", "{\n  \"a/b\": {},\n  \"x/y\": {}\n}\n", "{\n  \"x/y\": {}\n}\n", "{}"},
		{"only", "{\n  \"a/b\": {}\n}\n", "{}\n", "{}"},
		{"crlf", "{\r\n  \"x/y\": {},\r\n  \"a/b\": {}\r\n}\r\n", "{\r\n  \"x/y\": {}\r\n}\r\n", "{}"},
		{"one line", `{"x/y": {}, "a/b": {}}`, `{"x/y": {}}`, "{}"},
	} {
		out, removed, err := removeConfigSection([]byte(tc.in), "a/b")
		if err != nil || string(out) != tc.want || string(removed) != tc.removed {
			t.Errorf("%s: %q %q %v; want %q %q", tc.name, out, removed, err, tc.want, tc.removed)
		}
	}
	src := []byte("{\n  \"x/y\": {}\n}\n")
	if out, removed, err := removeConfigSection(src, "a/b"); err != nil || removed != nil || string(out) != string(src) {
		t.Errorf("absent section: %q %q %v", out, removed, err)
	}
}

func TestConfigSection_DuplicateKey(t *testing.T) {
	src := []byte("{\n  \"x/y\": {},\n  \"x/y\": {}\n}\n")
	want := `plugins-config.json: "x/y" appears twice`
	if _, _, err := addConfigSection(src, "a/b"); err == nil || err.Error() != want {
		t.Errorf("add: %v", err)
	}
	if _, _, err := removeConfigSection(src, "x/y"); err == nil || err.Error() != want {
		t.Errorf("remove: %v", err)
	}
}
