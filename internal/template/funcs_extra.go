package template

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

// The functions below take reflect.Value where a template may hand them a value
// of any type: html/template passes a reflect.Value parameter the argument as it
// is, so neither the signature nor the template checker needs an empty
// interface.

// ErrTemplateFuncArgs is returned by a built-in template function given an
// argument it cannot use: a JSON indent that is not a small integer, a list
// function handed a map, a division by zero.
var ErrTemplateFuncArgs = errors.New("collage: template function argument")

// maxSeq bounds {{seq n}}: a range over it allocates n integers, and a template
// that loops a million times is a bug, not a page.
const maxSeq = 100000

// maxIndent bounds json's and pre's indent, which is spaces per level.
const maxIndent = 16

// jsonFormat reads json's and pre's optional arguments: the spaces to indent by,
// 2 when absent, and whether to indent at all, true when absent.
func jsonFormat(name string, format []reflect.Value) (spaces int, pretty bool, err error) {
	spaces, pretty = 2, true
	if len(format) > 2 {
		return 0, false, fmt.Errorf("%w: %s takes a value, then at most spaces and pretty; got %d more", ErrTemplateFuncArgs, name, len(format))
	}
	if len(format) > 0 {
		v := indirect(format[0])
		if !v.IsValid() || !v.CanInt() {
			return 0, false, fmt.Errorf("%w: %s: spaces must be an integer, got %s", ErrTemplateFuncArgs, name, describeValue(format[0]))
		}
		if n := v.Int(); n < 0 || n > maxIndent {
			return 0, false, fmt.Errorf("%w: %s: spaces must be 0 to %d, got %d", ErrTemplateFuncArgs, name, maxIndent, n)
		}
		spaces = int(v.Int())
	}
	if len(format) > 1 {
		v := indirect(format[1])
		if !v.IsValid() || v.Kind() != reflect.Bool {
			return 0, false, fmt.Errorf("%w: %s: pretty must be true or false, got %s", ErrTemplateFuncArgs, name, describeValue(format[1]))
		}
		pretty = v.Bool()
	}
	return spaces, pretty, nil
}

// marshalJSON encodes v as json and pre do. encoding/json's HTML escaping stays
// on: <, > and & become <, > and &, so the result cannot close a
// <script> it is written into.
func marshalJSON(name string, v reflect.Value, format []reflect.Value) ([]byte, error) {
	spaces, pretty, err := jsonFormat(name, format)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if pretty {
		enc.SetIndent("", strings.Repeat(" ", spaces))
	}
	var encodeErr error
	if v.IsValid() {
		encodeErr = enc.Encode(v.Interface())
	} else {
		encodeErr = enc.Encode(nil)
	}
	if encodeErr != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTemplateFuncArgs, name, encodeErr)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// jsonFunc is {{json value [spaces] [pretty]}}: value as JSON, indented by two
// spaces unless told otherwise. It is template.JS, so inside a <script> — JSON
// data, or a script reading it — it is written as it is, and anywhere else
// html/template escapes it like any other text.
func jsonFunc(v reflect.Value, format ...reflect.Value) (template.JS, error) {
	b, err := marshalJSON("json", v, format)
	return template.JS(b), err
}

// pre is {{pre value [spaces] [pretty]}}: value in a <pre> block, for looking at
// what a template was given. A string or a []byte is shown as it is; anything
// else as json writes it. The content is always escaped.
func pre(v reflect.Value, format ...reflect.Value) (template.HTML, error) {
	var text string
	switch iv := indirect(v); {
	case iv.IsValid() && iv.Kind() == reflect.String:
		text = iv.String()
	case iv.IsValid() && iv.Kind() == reflect.Slice && iv.Type().Elem().Kind() == reflect.Uint8:
		text = string(iv.Bytes())
	default:
		b, err := marshalJSON("pre", v, format)
		if err != nil {
			return "", err
		}
		text = string(b)
	}
	return template.HTML("<pre>" + template.HTMLEscapeString(text) + "</pre>"), nil
}

func add(a, b int) int { return a + b }
func sub(a, b int) int { return a - b }
func mul(a, b int) int { return a * b }

func div(a, b int) (int, error) {
	if b == 0 {
		return 0, fmt.Errorf("%w: div %d by zero", ErrTemplateFuncArgs, a)
	}
	return a / b, nil
}

func mod(a, b int) (int, error) {
	if b == 0 {
		return 0, fmt.Errorf("%w: mod %d by zero", ErrTemplateFuncArgs, a)
	}
	return a % b, nil
}

func minInt(a, b int) int { return min(a, b) }
func maxInt(a, b int) int { return max(a, b) }

func addf(a, b float64) float64 { return a + b }
func subf(a, b float64) float64 { return a - b }
func mulf(a, b float64) float64 { return a * b }

func divf(a, b float64) (float64, error) {
	if b == 0 {
		return 0, fmt.Errorf("%w: divf %g by zero", ErrTemplateFuncArgs, a)
	}
	return a / b, nil
}

// The string functions take the string last, so each reads in a pipeline:
// {{.Title | replace "-" " " | trim}}.

func replace(old, with, s string) string { return strings.ReplaceAll(s, old, with) }
func contains(substr, s string) bool     { return strings.Contains(s, substr) }
func hasPrefix(prefix, s string) bool    { return strings.HasPrefix(s, prefix) }
func hasSuffix(suffix, s string) bool    { return strings.HasSuffix(s, suffix) }
func split(sep, s string) []string       { return strings.Split(s, sep) }

// truncate is {{truncate n s}}: s cut to its first n characters, with "…" after
// them when anything was cut. Characters, not bytes, so a Turkish letter is
// never split in half.
func truncate(n int, s string) (string, error) {
	if n < 0 {
		return "", fmt.Errorf("%w: truncate to %d characters", ErrTemplateFuncArgs, n)
	}
	if utf8.RuneCountInString(s) <= n {
		return s, nil
	}
	i := 0
	for range n {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + "…", nil
}

// list returns v as a slice or an array, or an error naming fn.
func list(fn string, v reflect.Value) (reflect.Value, error) {
	iv := indirect(v)
	if !iv.IsValid() || iv.Kind() != reflect.Slice && iv.Kind() != reflect.Array {
		return reflect.Value{}, fmt.Errorf("%w: %s wants a list, got %s", ErrTemplateFuncArgs, fn, describeValue(v))
	}
	return iv, nil
}

// first is {{first list}}: its first element, or no value when it is empty.
func first(v reflect.Value) (reflect.Value, error) {
	l, err := list("first", v)
	if err != nil || l.Len() == 0 {
		return reflect.Value{}, err
	}
	return l.Index(0), nil
}

// last is {{last list}}: its last element, or no value when it is empty.
func last(v reflect.Value) (reflect.Value, error) {
	l, err := list("last", v)
	if err != nil || l.Len() == 0 {
		return reflect.Value{}, err
	}
	return l.Index(l.Len() - 1), nil
}

// seq is {{range seq n}}: 1 through n, for a loop that counts. Nothing when n is
// below 1.
func seq(n int) ([]int, error) {
	if n > maxSeq {
		return nil, fmt.Errorf("%w: seq %d is over %d", ErrTemplateFuncArgs, n, maxSeq)
	}
	out := make([]int, 0, max(n, 0))
	for i := 1; i <= n; i++ {
		out = append(out, i)
	}
	return out, nil
}

// stringMap returns v as a map with string keys, or an error naming fn.
func stringMap(fn string, v reflect.Value) (reflect.Value, error) {
	iv := indirect(v)
	if !iv.IsValid() || iv.Kind() != reflect.Map || iv.Type().Key().Kind() != reflect.String {
		return reflect.Value{}, fmt.Errorf("%w: %s wants a map with string keys, got %s", ErrTemplateFuncArgs, fn, describeValue(v))
	}
	return iv, nil
}

// keys is {{keys map}}: the map's keys, sorted, so a loop over them renders the
// same way every time.
func keys(v reflect.Value) ([]string, error) {
	m, err := stringMap("keys", v)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, m.Len())
	for _, k := range m.MapKeys() {
		out = append(out, k.String())
	}
	slices.Sort(out)
	return out, nil
}

// hasKey is {{hasKey map "key"}}: whether the map holds key, even with a zero
// value under it.
func hasKey(v reflect.Value, key string) (bool, error) {
	m, err := stringMap("hasKey", v)
	if err != nil {
		return false, err
	}
	return m.MapIndex(reflect.ValueOf(key).Convert(m.Type().Key())).IsValid(), nil
}

// dig is {{dig value "a" "b" "c"}}: the value at that path through maps with
// string keys and the exported fields of structs, pointers followed. A path that
// leads nowhere is no value rather than an error, so {{with dig …}} and
// {{default}} handle what is optional.
func dig(v reflect.Value, path ...string) reflect.Value {
	cur := v
	for _, step := range path {
		cur = indirect(cur)
		switch {
		case !cur.IsValid():
			return reflect.Value{}
		case cur.Kind() == reflect.Map && cur.Type().Key().Kind() == reflect.String:
			cur = cur.MapIndex(reflect.ValueOf(step).Convert(cur.Type().Key()))
		case cur.Kind() == reflect.Struct:
			field, ok := cur.Type().FieldByName(step)
			if !ok || !field.IsExported() {
				return reflect.Value{}
			}
			// A promoted field behind a nil embedded pointer is a path that
			// leads nowhere too.
			next, err := cur.FieldByIndexErr(field.Index)
			if err != nil {
				return reflect.Value{}
			}
			cur = next
		default:
			return reflect.Value{}
		}
	}
	return cur
}

// indirect follows pointers and interfaces to the value they hold; a nil one is
// no value.
func indirect(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

// describeValue names v's type for an error message.
func describeValue(v reflect.Value) string {
	if iv := indirect(v); iv.IsValid() {
		return iv.Type().String()
	}
	return "nothing"
}

// withQueryPlaceholder is the parse-time stand-in for "withQuery", which needs
// the request being rendered and is bound per render.
func withQueryPlaceholder(key string, _ reflect.Value, _ ...reflect.Value) (string, error) {
	return "", fmt.Errorf("%w: withQuery %q", ErrURLOutsideRender, key)
}

// pathEscape is {{pathEscape s}}: s escaped to sit in one segment of a path.
func pathEscape(s string) string { return url.PathEscape(s) }
