package typecheck

import (
	"html/template"
	"io"
	"testing"
)

func TestRegressionExecutes(t *testing.T) {
	for src, data := range map[string]any{ // any: probe data of several types
		`{{.U.Edit}}`: page{&layout{}},
		`{{.Foo}}`:    pm{},
		`{{.Edit}}`:   outer{},
	} {
		if err := template.Must(template.New("x").Parse(src)).Execute(io.Discard, data); err != nil {
			t.Errorf("%s: %v", src, err)
		}
	}
}
