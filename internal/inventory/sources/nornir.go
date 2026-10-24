package sources

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

const NornirType = "nornir"

var NornirRequiredParams = []string{"path"}

// NornirSource parses a containerlab nornir inventory file.
type NornirSource struct {
	Params        map[string]string
	CredentialRef string
}

func ValidateNornirParams(params map[string]string, credentialRef string) error {
	return ValidateBase(NornirType, NornirRequiredParams, false, params, credentialRef)
}

func (s *NornirSource) Fetch() ([]Target, error) {
	path := s.Params["path"]
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &SourceError{Msg: fmt.Sprintf("nornir inventory not found: %s", path)}
		}
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data, err := parseYAML(string(raw))
	if err != nil {
		return nil, &SourceError{Msg: fmt.Sprintf("invalid nornir inventory %s: %v", path, err)}
	}
	root, ok := data.(*yamlMap)
	if !ok {
		return nil, &SourceError{Msg: fmt.Sprintf("nornir inventory %s must be a mapping of hosts", path)}
	}
	hosts := root
	if nested, ok := root.get("hosts").(*yamlMap); ok {
		hosts = nested
	}
	targets := make([]Target, 0, len(hosts.keys))
	for _, name := range hosts.keys {
		attrs, _ := hosts.get(name).(*yamlMap)
		var address any
		if attrs != nil {
			address = attrs.get("hostname")
			if !nonEmptyString(address) {
				address = attrs.get("address")
			}
		}
		var platform any
		if attrs != nil {
			platform = attrs.get("platform")
		}
		targets = append(targets, Target{
			Name:         name,
			Address:      optionalString(address),
			PlatformHint: optionalString(platform),
		})
	}
	return targets, nil
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

func optionalString(v any) *string {
	if v == nil {
		return nil
	}
	s := v.(string)
	return &s
}

// The inventory parser below is a deliberately small YAML reader.
//
// shortcut: only the block-mapping subset of YAML that nornir inventories use
// is parsed; flow collections (e.g. `groups: [leaf]`) stay opaque strings.
// Replace with gopkg.in/yaml.v3 once go.mod carries it.

type yamlMap struct {
	keys []string
	vals map[string]any
}

func (m *yamlMap) get(key string) any { return m.vals[key] }

func (m *yamlMap) set(key string, value any) {
	if _, ok := m.vals[key]; !ok {
		m.keys = append(m.keys, key)
	}
	m.vals[key] = value
}

type yamlLine struct {
	indent int
	text   string
}

func parseYAML(text string) (any, error) {
	lines, err := splitYAMLLines(text)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, nil
	}
	if isYAMLSeqItem(lines[0].text) {
		seq, _ := parseYAMLSeq(lines, 0, lines[0].indent)
		return seq, nil
	}
	m, _, err := parseYAMLMap(lines, 0, lines[0].indent)
	return m, err
}

func splitYAMLLines(text string) ([]yamlLine, error) {
	var lines []yamlLine
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") ||
			trimmed == "---" || trimmed == "..." {
			continue
		}
		body := strings.TrimLeft(raw, " ")
		if strings.HasPrefix(body, "\t") {
			return nil, errors.New("found a tab character where an indentation space is expected")
		}
		text := stripYAMLComment(strings.TrimSpace(raw))
		if text == "" {
			continue
		}
		lines = append(lines, yamlLine{indent: len(raw) - len(body), text: text})
	}
	return lines, nil
}

func stripYAMLComment(s string) string {
	var quote byte
	for i := range len(s) {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '#' && (i == 0 || s[i-1] == ' '):
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func parseYAMLMap(lines []yamlLine, i, indent int) (*yamlMap, int, error) {
	m := &yamlMap{vals: map[string]any{}}
	for i < len(lines) {
		line := lines[i]
		if line.indent < indent {
			break
		}
		if line.indent > indent {
			return nil, i, errors.New("unexpected indentation")
		}
		if isYAMLSeqItem(line.text) {
			break
		}
		key, rest, ok := splitYAMLKey(line.text)
		if !ok {
			return nil, i, errors.New("could not find expected ':'")
		}
		i++
		if rest != "" {
			m.set(key, parseYAMLScalar(rest))
			continue
		}
		if i < len(lines) && lines[i].indent > indent {
			childIndent := lines[i].indent
			if isYAMLSeqItem(lines[i].text) {
				var seq []any
				seq, i = parseYAMLSeq(lines, i, childIndent)
				m.set(key, seq)
			} else {
				child, next, err := parseYAMLMap(lines, i, childIndent)
				if err != nil {
					return nil, i, err
				}
				m.set(key, child)
				i = next
			}
			continue
		}
		m.set(key, nil)
	}
	return m, i, nil
}

func parseYAMLSeq(lines []yamlLine, i, indent int) ([]any, int) {
	var seq []any
	for i < len(lines) && lines[i].indent >= indent {
		line := lines[i]
		if line.indent == indent && isYAMLSeqItem(line.text) {
			seq = append(seq, parseYAMLScalar(strings.TrimSpace(strings.TrimPrefix(line.text, "-"))))
		}
		i++
	}
	return seq, i
}

func isYAMLSeqItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

func splitYAMLKey(s string) (key, rest string, ok bool) {
	var quote byte
	for i := range len(s) {
		c := s[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
		case c == ':' && (i+1 == len(s) || s[i+1] == ' '):
			return yamlString(strings.TrimSpace(s[:i])), strings.TrimSpace(s[i+1:]), true
		}
	}
	return "", "", false
}

func yamlString(s string) string {
	if v, ok := parseYAMLScalar(s).(string); ok {
		return v
	}
	return s
}

func parseYAMLScalar(s string) any {
	switch s {
	case "", "~", "null", "Null", "NULL":
		return nil
	}
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		if unquoted, err := strconv.Unquote(s); err == nil {
			return unquoted
		}
		return s[1 : len(s)-1]
	}
	return s
}
