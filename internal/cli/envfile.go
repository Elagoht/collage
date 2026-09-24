package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// devEnvFiles are the files "collage dev" reads variables from, in order of
// preference. Only the first one found is read: .env.development replaces .env
// rather than being merged over it, because merging is a second rule to explain
// for very little gain.
var devEnvFiles = []string{".env.development", ".env"}

// ErrEnvFile reports a line in an environment file that is not KEY=value.
//
// A malformed line is an error rather than a skipped one. A skipped line is a
// setting somebody wrote and the program never saw, and the symptom — a default
// where a value was meant to be — points everywhere except at the file.
var ErrEnvFile = errors.New("collage: malformed environment file")

// loadDevEnv reads the first of devEnvFiles found in dir and returns its
// variables as KEY=value pairs, leaving out any key lookup finds already set, so
// a variable in the shell wins over the file. It returns the name of the file it
// read, or "" when there was none — which is not an error.
func loadDevEnv(dir string, lookup func(string) (string, bool)) ([]string, string, error) {
	for _, name := range devEnvFiles {
		content, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, "", err
		}
		pairs, err := parseEnvFile(name, string(content))
		if err != nil {
			return nil, "", err
		}
		var env []string
		for _, pair := range pairs {
			if _, set := lookup(pair[0]); set {
				continue
			}
			env = append(env, pair[0]+"="+pair[1])
		}
		return env, name, nil
	}
	return nil, "", nil
}

// parseEnvFile parses content, the text of the file called name, into key and
// value pairs in file order.
//
// The format is the common subset of what dotenv files are: KEY=value lines,
// blank lines, lines starting with "#", an optional "export " prefix, and an
// optional pair of matching single or double quotes around the value, which are
// removed and nothing inside them interpreted. An unquoted value ends at a "#"
// preceded by whitespace, so "PORT=3000 # dev" is 3000 rather than a port that
// fails to parse and quietly falls back to the default.
func parseEnvFile(name, content string) ([][2]string, error) {
	var pairs [][2]string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for number := 1; scanner.Scan(); number++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fail := func(reason string) error {
			return fmt.Errorf("%w: %s:%d: %s", ErrEnvFile, name, number, reason)
		}

		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fail("want KEY=value")
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return nil, fail(fmt.Sprintf("%q is not a variable name", key))
		}

		value = strings.TrimSpace(value)
		if value != "" && (value[0] == '"' || value[0] == '\'') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, fail("unterminated quote")
			}
			rest := strings.TrimSpace(value[end+2:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, fail("text after the closing quote")
			}
			value = value[1 : end+1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		} else if i := strings.Index(value, "\t#"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}

		pairs = append(pairs, [2]string{key, value})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("collage: read %s: %w", name, err)
	}
	return pairs, nil
}

// validEnvKey reports whether key is a portable environment variable name:
// letters, digits and underscores, not starting with a digit.
func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
