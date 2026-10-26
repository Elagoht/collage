package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"strings"
)

// agentsTemplate is the section of a project's AGENTS.md that collage writes:
// the rules a coding agent needs to work in a collage project, between the
// markers mergeAgents finds again. One file, so "new --ai" and "ai init" cannot
// write two different versions of it.
//
//go:embed ai/AGENTS.md
var agentsTemplate string

// ErrAgentsMarkers is returned when an AGENTS.md has collage's markers, but not
// as one begin line followed by one end line: which text is collage's is then a
// guess, and a wrong guess deletes the user's rules.
var ErrAgentsMarkers = errors.New("collage: AGENTS.md markers")

// The lines that open and close collage's section. A line is a marker when it
// starts with one, so the begin marker can carry the version that wrote it.
const (
	agentsBegin = "<!-- collage:begin"
	agentsEnd   = "<!-- collage:end -->"
)

// projectRulesStub follows collage's section in a new AGENTS.md: the place the
// user's own rules go, which collage never writes again.
const projectRulesStub = "\n## Project rules\n\n<!-- Your own rules for agents. Collage never changes anything outside the markers above. -->\n"

// managedSection returns collage's section of AGENTS.md as version writes it.
func managedSection(version string) []byte {
	return []byte(strings.ReplaceAll(agentsTemplate, "@@VERSION@@", version))
}

// mergeAgents returns existing with collage's section replaced by section. A
// nil existing is a project without an AGENTS.md: it gets the section and an
// empty place for its own rules. One without markers keeps all of its text,
// below the section. Everything outside the markers is kept byte for byte.
func mergeAgents(existing, section []byte) ([]byte, error) {
	if existing == nil {
		return append(section[:len(section):len(section)], projectRulesStub...), nil
	}
	text := string(existing)
	begin, end := -1, -1 // byte offsets: the begin line's start, the end line's end
	offset := 0
	for line := range strings.SplitAfterSeq(text, "\n") {
		bare := strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(bare, agentsBegin):
			if begin >= 0 {
				return nil, fmt.Errorf("%w: a second begin marker", ErrAgentsMarkers)
			}
			begin = offset
		case strings.HasPrefix(bare, agentsEnd):
			if begin < 0 {
				return nil, fmt.Errorf("%w: an end marker before any begin marker", ErrAgentsMarkers)
			}
			if end >= 0 {
				return nil, fmt.Errorf("%w: a second end marker", ErrAgentsMarkers)
			}
			end = offset + len(line)
		}
		offset += len(line)
	}
	switch {
	case begin < 0:
		return append(append(section[:len(section):len(section)], '\n'), existing...), nil
	case end < 0:
		return nil, fmt.Errorf("%w: a begin marker without an end marker", ErrAgentsMarkers)
	}
	return []byte(text[:begin] + string(section) + text[end:]), nil
}
