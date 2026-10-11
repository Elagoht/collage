package plugincontract

import "fmt"

// Plugin is what Check learned about a module that meets the contract.
type Plugin struct {
	Manifest    Manifest
	Constructor Constructor
}

// refuse is a contract refusal: the reason, and where the contract is written.
// Callers format the reason with fmt.Sprintf first, so no variadic parameter of
// our own takes arbitrary values.
func refuse(reason string) error {
	return fmt.Errorf("%w: %s; see %s", errContract, reason, AuthorsURL)
}

// Check checks rules 1–4 on the module at dir. short is the reference's
// Ref.Short; when it is not "", the manifest's name must equal it.
func Check(dir, short string) (Plugin, error) {
	m, err := ReadManifest(dir)
	if err != nil {
		return Plugin{}, err
	}
	if short != "" && m.Name != short {
		return Plugin{}, refuse(fmt.Sprintf("collage.json names the plugin %q, not %q", m.Name, short))
	}
	c, err := ReadConstructor(dir)
	if err != nil {
		return Plugin{}, err
	}
	return Plugin{Manifest: m, Constructor: c}, nil
}
