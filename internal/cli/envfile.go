package cli

import "github.com/Elagoht/collage/internal/envfile"

// devEnvFiles are the files "collage dev" reads variables from, in order of
// preference. Only the first one found is read: .env.development replaces .env
// rather than being merged over it, because merging is a second rule to explain
// for very little gain.
var devEnvFiles = []string{".env.development", ".env"}

// ErrEnvFile reports a line in an environment file that is not KEY=value.
var ErrEnvFile = envfile.ErrMalformed

// loadDevEnv reads the first of devEnvFiles found in dir; see envfile.Load.
func loadDevEnv(dir string, lookup func(string) (string, bool)) ([]string, string, error) {
	return envfile.Load(dir, devEnvFiles, lookup)
}
