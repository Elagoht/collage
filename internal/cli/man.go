package cli

import "flag"

// defineManFlags declares man's flags on fs: whether to install the page
// rather than print it. runMan parses them; completion reads the same
// declaration.
func defineManFlags(fs *flag.FlagSet) (install *bool) {
	return fs.Bool("install", false, "write the page where man finds it, beside the collage binary")
}
