package httpx

import (
	"errors"
	"strings"
	"text/template"
)

// devCause picks out of err what a development page should lead with: the error
// a template function returned, and the template, line and column it was called
// from.
//
// A render failure reaches the error path wrapped once for every template it
// passes through on its way up — a layout, a page, each fragment between — and
// its message is all of them on one line, outermost first. The cause is the tail
// of that line. It is found through the innermost template.ExecError, which is
// where the template engine recorded the call that failed.
//
// Without an ExecError in the chain — a data handler's error, a hook's — there is
// nothing to pick apart, and ok is false.
func devCause(err error) (cause, where string, ok bool) {
	// The engine returns ExecError by value, and each layer above wraps with %w,
	// so one walk down the chain meets every execution, outermost first.
	var innermost template.ExecError
	found := false
	for e := err; e != nil; e = errors.Unwrap(e) {
		if exec, isExec := e.(template.ExecError); isExec {
			innermost, found = exec, true
		}
	}
	if !found {
		return "", "", false
	}

	// "template: pages/recipe.html:9:4: executing ..." — the location is the
	// first field after the prefix, and has no ": " in it.
	message := strings.TrimPrefix(innermost.Error(), "template: ")
	where, rest, _ := strings.Cut(message, ": ")

	// A failed call is wrapped as "error calling slot: <what it returned>"; a
	// template's own mistake, a missing field, has nothing beneath it and is
	// its own cause.
	if returned := errors.Unwrap(innermost.Err); returned != nil {
		return returned.Error(), where, true
	}
	return rest, where, true
}
