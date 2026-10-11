package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

var errConfigNotObject = errors.New("plugins-config.json is not a JSON object")

// configMember is one top-level member of plugins-config.json, by offsets.
type configMember struct {
	name               string
	keyStart           int // the key's opening quote
	valueStart, valEnd int // the value, without surrounding whitespace
}

// configObject is the top-level object: its members, and the offsets of its
// braces.
type configObject struct {
	members     []configMember
	open, close int
}

func parseConfigObject(src []byte) (configObject, error) {
	var obj configObject
	dec := json.NewDecoder(bytes.NewReader(src))
	tok, err := dec.Token()
	if err != nil {
		return obj, fmt.Errorf("plugins-config.json: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return obj, errConfigNotObject
	}
	obj.open = int(dec.InputOffset()) - 1
	for dec.More() {
		at := skipJSON(src, int(dec.InputOffset()), ", \t\r\n")
		tok, err := dec.Token()
		if err != nil {
			return obj, fmt.Errorf("plugins-config.json: %w", err)
		}
		name, _ := tok.(string)
		vs := skipJSON(src, int(dec.InputOffset()), ": \t\r\n")
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return obj, fmt.Errorf("plugins-config.json: %w", err)
		}
		obj.members = append(obj.members, configMember{name, at, vs, int(dec.InputOffset())})
	}
	if _, err := dec.Token(); err != nil {
		return obj, fmt.Errorf("plugins-config.json: %w", err)
	}
	obj.close = int(dec.InputOffset()) - 1
	if obj.close < 0 || obj.close >= len(src) || src[obj.close] != '}' {
		return obj, errConfigNotObject
	}
	if !blank(src[obj.close+1:]) {
		return obj, errors.New("plugins-config.json: something follows the object")
	}
	return obj, nil
}

// skipJSON is the first offset from i whose byte is not in set.
func skipJSON(src []byte, i int, set string) int {
	for i < len(src) && bytes.IndexByte([]byte(set), src[i]) >= 0 {
		i++
	}
	return i
}

func configNewline(src []byte) string {
	if bytes.Contains(src, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

// addConfigSection adds `"name": {}` as the object's last member, in the
// file's own indentation and line endings. A section already there is left
// alone, and added is false.
func addConfigSection(src []byte, name string) (out []byte, added bool, err error) {
	key, err := json.Marshal(name)
	if err != nil {
		return nil, false, err
	}
	member := string(key) + ": {}"
	if src == nil {
		return []byte("{\n  " + member + "\n}\n"), true, nil
	}
	obj, err := parseConfigObject(src)
	if err != nil {
		return nil, false, err
	}
	for _, m := range obj.members {
		if m.name == name {
			return src, false, nil
		}
	}
	nl := configNewline(src)
	var at, end int
	var text string
	if len(obj.members) == 0 {
		at, end = obj.open+1, obj.close
		text = nl + "  " + member + nl
	} else {
		first, last := obj.members[0], obj.members[len(obj.members)-1]
		at, end = last.valEnd, last.valEnd
		if !bytes.ContainsRune(src[obj.open:obj.close], '\n') {
			text = ", " + member
		} else {
			ls := bytes.LastIndexByte(src[:first.keyStart], '\n') + 1
			indent := string(src[ls:first.keyStart])
			if !blank([]byte(indent)) {
				indent = "  "
			}
			text = "," + nl + indent + member
		}
	}
	out = append(append(append([]byte(nil), src[:at]...), text...), src[end:]...)
	return out, true, nil
}

// removeConfigSection cuts the member named name out of the object, and
// returns its raw value; removed is nil when there is no such member.
func removeConfigSection(src []byte, name string) (out []byte, removed []byte, err error) {
	obj, err := parseConfigObject(src)
	if err != nil {
		return nil, nil, err
	}
	i := -1
	for j, m := range obj.members {
		if m.name == name {
			i = j
			break
		}
	}
	if i < 0 {
		return src, nil, nil
	}
	m := obj.members[i]
	removed = append([]byte(nil), bytes.TrimSpace(src[m.valueStart:m.valEnd])...)
	var at, end int
	switch {
	case len(obj.members) == 1:
		at, end = obj.open+1, obj.close
	case i < len(obj.members)-1:
		at, end = m.keyStart, obj.members[i+1].keyStart
	default:
		at, end = obj.members[i-1].valEnd, m.valEnd
	}
	out = append(append([]byte(nil), src[:at]...), src[end:]...)
	return out, removed, nil
}
