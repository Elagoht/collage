package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvFile_Format(t *testing.T) {
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
	if _, err := parseEnvFile(".env", content); err == nil || !strings.Contains(err.Error(), ".env:7") {
		t.Fatalf("parseEnvFile() error = %v, want one naming .env:7", err)
	}

	content = strings.Replace(content, `SINGLE='it''s'`, `SINGLE='it is'`, 1)
	pairs, err := parseEnvFile(".env", content)
	if err != nil {
		t.Fatalf("parseEnvFile() = %v, want nil", err)
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
func TestParseEnvFile_MalformedLines(t *testing.T) {
	for _, line := range []string{
		"JUST_A_WORD",
		"1PORT=3000",
		"MY-KEY=x",
		"=value",
		`OPEN="never closed`,
	} {
		t.Run(line, func(t *testing.T) {
			_, err := parseEnvFile(".env.development", "OK=1\n"+line+"\n")
			if !errors.Is(err, ErrEnvFile) {
				t.Fatalf("parseEnvFile() error = %v, want ErrEnvFile", err)
			}
			if !strings.Contains(err.Error(), ".env.development:2") {
				t.Errorf("error = %q, want it to name .env.development:2", err)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// .env.development replaces .env; the two are never merged.
func TestLoadDevEnv_PrefersDevelopmentAndDoesNotMerge(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "FROM_DOTENV=1\nSHARED=dotenv\n")
	writeFile(t, filepath.Join(dir, ".env.development"), "SHARED=development\n")

	env, loaded, err := loadDevEnv(dir, func(string) (string, bool) { return "", false })
	if err != nil {
		t.Fatalf("loadDevEnv() = %v, want nil", err)
	}
	if loaded != ".env.development" {
		t.Errorf("loaded = %q, want .env.development", loaded)
	}
	if !reflect.DeepEqual(env, []string{"SHARED=development"}) {
		t.Errorf("env = %q, want only .env.development's variables", env)
	}
}

func TestLoadDevEnv_FallsBackToDotEnv(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "PORT=3000\n")

	env, loaded, err := loadDevEnv(dir, func(string) (string, bool) { return "", false })
	if err != nil || loaded != ".env" || !reflect.DeepEqual(env, []string{"PORT=3000"}) {
		t.Errorf("loadDevEnv() = %q, %q, %v; want [PORT=3000], .env, nil", env, loaded, err)
	}
}

func TestLoadDevEnv_NoFileIsNotAnError(t *testing.T) {
	env, loaded, err := loadDevEnv(t.TempDir(), os.LookupEnv)
	if err != nil || loaded != "" || env != nil {
		t.Errorf("loadDevEnv() = %q, %q, %v; want nothing and no error", env, loaded, err)
	}
}

// PORT=4000 collage dev still means port 4000.
func TestLoadDevEnv_TheShellWins(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "PORT=3000\nHOST=localhost\n")

	shell := map[string]string{"PORT": "4000"}
	env, _, err := loadDevEnv(dir, func(key string) (string, bool) {
		value, ok := shell[key]
		return value, ok
	})
	if err != nil {
		t.Fatalf("loadDevEnv() = %v, want nil", err)
	}
	if !reflect.DeepEqual(env, []string{"HOST=localhost"}) {
		t.Errorf("env = %q, want PORT left to the shell", env)
	}
}

// End to end through "collage dev": the file's variables reach the command, the
// file is named, and COLLAGE_DEV=1 comes last so the file cannot turn it off.
func TestRun_Dev_LoadsTheEnvFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env.development"), "COLLAGE_TEST_KEY=abc\nCOLLAGE_DEV=0\n")
	t.Chdir(dir)

	c, _, errOut := testCLI()
	runner := &fakeRunner{}
	c.Runner = runner

	if code := c.Run(context.Background(), []string{"dev"}); code != 0 {
		t.Fatalf("exit = %d, want 0; stderr:\n%s", code, errOut.String())
	}
	want := []string{"COLLAGE_TEST_KEY=abc", "COLLAGE_DEV=0", "COLLAGE_DEV=1"}
	if !reflect.DeepEqual(runner.env, want) {
		t.Errorf("env = %q, want %q", runner.env, want)
	}
	if !strings.Contains(errOut.String(), "loaded .env.development") {
		t.Errorf("stderr = %q, want it to name the file it loaded", errOut.String())
	}
}

func TestRun_Dev_AMalformedFileStopsIt(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "PORT 3000\n")
	t.Chdir(dir)

	c, _, errOut := testCLI()
	runner := &fakeRunner{}
	c.Runner = runner

	if code := c.Run(context.Background(), []string{"dev"}); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if runner.name != "" {
		t.Errorf("the project was started anyway")
	}
	if !strings.Contains(errOut.String(), ".env:1") {
		t.Errorf("stderr = %q, want it to name .env:1", errOut.String())
	}
}
