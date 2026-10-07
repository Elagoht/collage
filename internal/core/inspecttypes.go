package core

import (
	"reflect"
	"runtime/debug"
	"sort"
	"strings"
)

// InspectedType is the shape of a type a fragment's template can reach, for an
// editor completing {{.}}: its exported fields and the exported methods of it
// and its pointer. A named pointer, slice, array, map or chan also names what
// it holds: Elem, and Key for a map.
type InspectedType struct {
	Kind    string            `json:"kind"`
	Key     string            `json:"key,omitempty"`
	Elem    string            `json:"elem,omitempty"`
	Fields  []InspectedField  `json:"fields,omitempty"`
	Methods []InspectedMethod `json:"methods,omitempty"`
}

// InspectedField is one exported field, promoted ones included. An embedded
// field is listed too, as a template reaches it by its type's name ({{.Base}}),
// and marked Embedded.
type InspectedField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Embedded bool   `json:"embedded,omitempty"`
}

// InspectedMethod is one exported method a template can call.
type InspectedMethod struct {
	Name    string `json:"name"`
	Args    int    `json:"args"`
	Returns string `json:"returns"`
}

// typeTable describes every named type reachable from roots through fields,
// element types and method results, keyed by its Go name ("blog.Post"). A
// standard library type is opaque: a template's author does not need time.Time
// spelled out, and the table stays the size of the application.
func typeTable(roots []reflect.Type) map[string]InspectedType {
	table := make(map[string]InspectedType)
	seen := make(map[reflect.Type]bool)
	var visit func(t reflect.Type)
	// register lists t (a named type, or an unnamed struct) and visits what it reaches.
	register := func(t reflect.Type) {
		seen[t] = true
		entry := InspectedType{Kind: t.Kind().String()}
		switch t.Kind() {
		case reflect.Struct:
			for _, f := range reflect.VisibleFields(t) {
				if f.IsExported() {
					entry.Fields = append(entry.Fields, InspectedField{Name: f.Name, Type: f.Type.String(), Embedded: f.Anonymous})
					visit(f.Type)
				}
			}
		case reflect.Map:
			entry.Key = t.Key().String()
			entry.Elem = t.Elem().String()
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			entry.Elem = t.Elem().String()
		}
		pt := reflect.PointerTo(t)
		for i := range pt.NumMethod() {
			m := pt.Method(i)
			if m.Type.NumOut() == 0 {
				continue
			}
			entry.Methods = append(entry.Methods, InspectedMethod{Name: m.Name, Args: m.Type.NumIn() - 1, Returns: m.Type.Out(0).String()})
			visit(m.Type.Out(0))
		}
		sort.Slice(entry.Methods, func(i, j int) bool { return entry.Methods[i].Name < entry.Methods[j].Name })
		table[t.String()] = entry
	}
	visit = func(t reflect.Type) {
		if seen[t] {
			return
		}
		if t.Name() != "" && isStandard(t) {
			return
		}
		named := t.Name() != ""
		if named || t.Kind() == reflect.Struct {
			register(t)
		} else {
			seen[t] = true
		}
		// A container, named or not, also reaches its element and key types.
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			visit(t.Elem())
		case reflect.Map:
			visit(t.Key())
			visit(t.Elem())
		}
	}
	for _, root := range roots {
		if root != nil {
			visit(root)
		}
	}
	return table
}

// modules are the paths of the running program's main module and every module
// it depends on: a package under one of them is not the standard library,
// whatever its path looks like — `module sen-de-yaz` has no dot in it.
var modules = func() []string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	out := []string{info.Main.Path}
	for _, dep := range info.Deps {
		out = append(out, dep.Path)
	}
	return out
}()

// isStandard reports a type from the standard library: one in no module the
// program is built from, not in package main, and whose path's first element
// has no dot.
func isStandard(t reflect.Type) bool {
	path := t.PkgPath()
	if path == "" {
		return true
	}
	if path == "main" {
		return false
	}
	for _, m := range modules {
		if m != "" && (path == m || strings.HasPrefix(path, m+"/")) {
			return false
		}
	}
	first, _, _ := strings.Cut(path, "/")
	return !strings.Contains(first, ".")
}
