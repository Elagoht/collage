// Package main must not compile: every line below is a type error the typed keys
// exist to catch. keytypes_test.go builds it and expects each one.
package main

import (
	"context"

	"github.com/Elagoht/collage/pkg/collage"
)

var countKey = collage.NewKey[int]("count")

func main() {
	var rc *collage.RenderContext
	countKey.Set(rc, "seven")                                                                   // WANT: cannot use "seven"
	_, _ = collage.Once(rc, countKey, func(context.Context) (string, error) { return "", nil }) // WANT: type func
	var s string
	s, _ = countKey.Get(rc) // WANT: cannot use
	_ = s
}
