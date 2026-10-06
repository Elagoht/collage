package typecheck

import "text/template/parse"

// call evaluates a function call. Functions are checked in a later step; until
// then every argument is still walked, and the result is unknown.
func (w *walker) call(ident *parse.IdentifierNode, args []parse.Node, dot value, vars []variable, hasFinal bool, final value) value {
	for _, arg := range args {
		w.arg(arg, dot, vars)
	}
	return unknown
}
