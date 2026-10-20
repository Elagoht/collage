package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

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
