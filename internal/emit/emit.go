// Package emit serializes a DesiredConfig to the gnmic YAML files. The dump
// mirrors PyYAML's safe_dump output (block style, indentless sequences, 80-column
// folding, sort_keys=False) so rendered files are byte-identical to Python's.
package emit

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/fatred/gnpctl-v2/internal/render"
	"gopkg.in/yaml.v3"
)

// PromOutput is the egress output name the rendered inputs write to.
const PromOutput = "prometheus"

// SkipVerifyBanner is prepended to ingress.yaml when skip-verify is true, so the
// disabled gNMI server-cert check is never silent in the rendered artifact.
const SkipVerifyBanner = "# ============================================================================\n" +
	"# WARNING: skip-verify is TRUE — gNMI server-certificate validation is DISABLED.\n" +
	"# This trusts ANY certificate on every device subscription (MITM-exposed).\n" +
	"# Prefer `render.gnmic.tls_ca: <ca-bundle>` to pin a private CA instead.\n" +
	"# ============================================================================\n"

// Emitter writes the rendered artifacts and returns the written paths.
type Emitter interface {
	Emit(desired *render.DesiredConfig) ([]string, error)
}

// FileEmitter serializes a DesiredConfig to ingress.yaml / egress.yaml /
// targets.yaml under OutputDir.
type FileEmitter struct {
	OutputDir string
}

// NewFileEmitter returns a FileEmitter writing to dir.
func NewFileEmitter(dir string) *FileEmitter { return &FileEmitter{OutputDir: dir} }

// Emit writes the three artifacts in Python's order and returns their paths.
func (e *FileEmitter) Emit(desired *render.DesiredConfig) ([]string, error) {
	if err := os.MkdirAll(e.OutputDir, 0o777); err != nil {
		return nil, err
	}
	artifacts := []struct {
		name string
		doc  render.Ordered
	}{
		{"ingress.yaml", ingressDoc(desired)},
		{"targets.yaml", targetsDoc(desired)}, // isolated channel (hybrid-ready)
		{"egress.yaml", egressDoc(desired)},
	}
	written := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		path := filepath.Join(e.OutputDir, artifact.name)
		text, err := dump(artifact.doc)
		if err != nil {
			return nil, err
		}
		// Never let a TLS-disabling render be silent: banner the file + warn.
		if artifact.name == "ingress.yaml" && artifact.doc.Keys[0] == "skip-verify" &&
			artifact.doc.Values[0] == true {
			text = SkipVerifyBanner + text
			fmt.Fprintf(os.Stderr,
				"warning: %s sets skip-verify: true — gNMI device-certificate "+
					"validation is DISABLED. Prefer render.gnmic.tls_ca to pin a CA.\n", path)
		}
		if err := os.WriteFile(path, []byte(text), 0o666); err != nil {
			return nil, err
		}
		written = append(written, path)
	}
	return written, nil
}

func ingressDoc(desired *render.DesiredConfig) render.Ordered {
	g := desired.Ingress.Global
	doc := render.Ordered{}
	doc.Set("skip-verify", g.SkipVerify)
	if g.TLSCA != nil && *g.TLSCA != "" {
		doc.Set("tls-ca", *g.TLSCA)
	}
	// Credentials are env references gnmic expands at runtime — never literals.
	doc.Set("username", g.UsernameRef)
	doc.Set("password", g.PasswordRef)
	if g.APIServer != nil && *g.APIServer != "" {
		server := render.Ordered{}
		server.Set("address", *g.APIServer)
		doc.Set("api-server", server)
	}
	loader := render.Ordered{}
	loader.Set("type", "file")
	loader.Set("path", g.TargetsFile)
	doc.Set("loader", loader)

	subscriptions := render.Ordered{}
	for _, sub := range desired.Ingress.Subscriptions {
		streamSubs := make([]any, 0, len(sub.StreamSubscriptions))
		for _, ss := range sub.StreamSubscriptions {
			entry := render.Ordered{}
			entry.Set("paths", ss.Paths)
			entry.Set("stream-mode", ss.Mode)
			if ss.SampleInterval != nil && *ss.SampleInterval != "" {
				entry.Set("sample-interval", *ss.SampleInterval)
			}
			streamSubs = append(streamSubs, entry)
		}
		entry := render.Ordered{}
		entry.Set("stream-subscriptions", streamSubs)
		entry.Set("outputs", sub.Outputs)
		subscriptions.Set(sub.Name, entry)
	}
	outputs := render.Ordered{}
	for _, out := range desired.Ingress.Outputs {
		entry := render.Ordered{}
		entry.Set("type", "jetstream")
		entry.Set("address", out.Address)
		entry.Set("stream", out.Stream)
		entry.Set("subject-format", out.SubjectFormat)
		entry.Set("format", out.Format)
		entry.Set("num-workers", out.NumWorkers)
		entry.Set("write-timeout", out.WriteTimeout)
		entry.Set("connect-time-wait", out.ConnectTimeWait)
		entry.Set("create-stream", out.CreateStream)
		outputs.Set(out.Name, entry)
	}
	doc.Set("subscriptions", subscriptions)
	doc.Set("outputs", outputs)
	return doc
}

func targetsDoc(desired *render.DesiredConfig) render.Ordered {
	doc := render.Ordered{}
	for _, t := range desired.Targets.Targets {
		entry := render.Ordered{}
		if t.Address != nil && *t.Address != "" {
			if t.Port != nil && *t.Port != 0 {
				entry.Set("address", fmt.Sprintf("%s:%d", *t.Address, *t.Port))
			} else {
				entry.Set("address", *t.Address)
			}
		}
		if t.Encoding != nil && *t.Encoding != "" {
			entry.Set("encoding", *t.Encoding)
		}
		entry.Set("subscriptions", t.Subscriptions)
		tags := render.Ordered{}
		for i, key := range t.Tags.Keys {
			if t.Tags.Values[i] == nil {
				continue
			}
			tags.Set(key, t.Tags.Values[i])
		}
		if len(tags.Keys) > 0 {
			entry.Set("tags", tags)
		}
		doc.Set(t.Name, entry)
	}
	return doc
}

func egressDoc(desired *render.DesiredConfig) render.Ordered {
	egress := &desired.Egress
	tuning := egress.InputTuning
	inputs := render.Ordered{}
	for _, consumer := range egress.Consumers {
		entry := render.Ordered{}
		entry.Set("type", "jetstream")
		entry.Set("address", egress.NatsAddress)
		entry.Set("stream", consumer.Stream)
		entry.Set("format", tuning.Format)
		entry.Set("deliver-policy", tuning.DeliverPolicy)
		entry.Set("num-workers", tuning.NumWorkers)
		entry.Set("buffer-size", tuning.BufferSize)
		entry.Set("fetch-batch-size", tuning.FetchBatchSize)
		entry.Set("max-ack-pending", tuning.MaxAckPending)
		entry.Set("event-processors", consumer.Processors)
		entry.Set("outputs", []string{PromOutput})
		inputs.Set(consumer.Name, entry)
	}
	prom := egress.Prometheus
	promEntry := render.Ordered{}
	promEntry.Set("type", "prometheus_write")
	promEntry.Set("url", egress.PrometheusRemoteWriteURL)
	promEntry.Set("interval", prom.Interval)
	promEntry.Set("buffer-size", prom.BufferSize)
	promEntry.Set("max-time-series-per-write", prom.MaxTimeSeriesPerWrite)
	promEntry.Set("timeout", prom.Timeout)
	promEntry.Set("num-workers", prom.NumWorkers)
	outputs := render.Ordered{}
	outputs.Set(PromOutput, promEntry)

	doc := render.Ordered{}
	doc.Set("inputs", inputs)
	doc.Set("outputs", outputs)
	if len(egress.Processors.Keys) > 0 {
		doc.Set("processors", egress.Processors)
	}
	return doc
}

// ----- PyYAML-compatible dumper -----

func dump(doc render.Ordered) (string, error) {
	w := &yamlWriter{whitespace: true, indention: true, bestWidth: 80}
	if err := w.emitMapping(doc, 0); err != nil {
		return "", err
	}
	w.writeLineBreak()
	return w.buf.String(), nil
}

type yamlWriter struct {
	buf        strings.Builder
	column     int
	whitespace bool
	indention  bool
	bestWidth  int
}

func (w *yamlWriter) emitMapping(mapping render.Ordered, indent int) error {
	for i, key := range mapping.Keys {
		w.writeIndent(indent)
		w.writeScalar(key, indent, false)
		w.writeIndicator(":", false, false, false)
		if err := w.emitValue(mapping.Values[i], indent); err != nil {
			return err
		}
	}
	return nil
}

// emitValue writes a node in block mapping-value position (inline for scalars
// and empty collections, next line for non-empty collections).
func (w *yamlWriter) emitValue(value any, indent int) error {
	switch v := value.(type) {
	case *yaml.Node:
		return w.emitValue(nodeValue(v), indent)
	case render.Ordered:
		return w.emitMappingValue(v, indent)
	case *render.Ordered:
		if v == nil {
			w.writePlain("null", indent+2, true)
			return nil
		}
		return w.emitMappingValue(*v, indent)
	case []any:
		return w.emitSequenceValue(v, indent)
	case []string:
		return w.emitSequenceValue(stringsToAny(v), indent)
	default:
		return w.emitScalar(value, indent+2, true)
	}
}

func (w *yamlWriter) emitMappingValue(mapping render.Ordered, indent int) error {
	if len(mapping.Keys) == 0 {
		w.writeIndicator("{}", true, false, false)
		return nil
	}
	w.writeLineBreak()
	return w.emitMapping(mapping, indent+2)
}

func (w *yamlWriter) emitSequenceValue(seq []any, indent int) error {
	if len(seq) == 0 {
		w.writeIndicator("[]", true, false, false)
		return nil
	}
	w.writeLineBreak()
	return w.emitSequence(seq, indent)
}

// emitSequence writes block sequence items. A sequence in mapping-value position
// is indentless, so items sit at the mapping key's own indentation.
func (w *yamlWriter) emitSequence(seq []any, indent int) error {
	for _, item := range seq {
		w.writeIndent(indent)
		w.writeIndicator("-", true, false, true)
		switch v := item.(type) {
		case *yaml.Node:
			if err := w.emitSequenceItem(nodeValue(v), indent); err != nil {
				return err
			}
		default:
			if err := w.emitSequenceItem(item, indent); err != nil {
				return err
			}
		}
	}
	return nil
}

func (w *yamlWriter) emitSequenceItem(value any, indent int) error {
	switch v := value.(type) {
	case render.Ordered:
		if len(v.Keys) == 0 {
			w.writeIndicator("{}", true, false, false)
			return nil
		}
		return w.emitMapping(v, indent+2)
	case *render.Ordered:
		if v == nil {
			w.writePlain("null", indent+2, true)
			return nil
		}
		return w.emitSequenceItem(*v, indent)
	case []any:
		if len(v) == 0 {
			w.writeIndicator("[]", true, false, false)
			return nil
		}
		return w.emitSequence(v, indent+2)
	case []string:
		return w.emitSequenceItem(stringsToAny(v), indent)
	default:
		return w.emitScalar(value, indent+2, true)
	}
}

func (w *yamlWriter) emitScalar(value any, indent int, split bool) error {
	switch v := value.(type) {
	case nil:
		w.writePlain("null", indent, split)
	case bool:
		w.writePlain(strconv.FormatBool(v), indent, split)
	case int:
		w.writePlain(strconv.Itoa(v), indent, split)
	case int64:
		w.writePlain(strconv.FormatInt(v, 10), indent, split)
	case float64:
		text := strconv.FormatFloat(v, 'g', -1, 64)
		w.writePlain(text, indent, split)
	case string:
		w.writeScalar(v, indent, split)
	default:
		return fmt.Errorf("yaml: cannot dump %T", value)
	}
	return nil
}

// writeScalar writes a string choosing PyYAML's scalar style: plain when
// unambiguous, else single-quoted, else double-quoted.
func (w *yamlWriter) writeScalar(text string, indent int, split bool) {
	if text == "" {
		w.writeSingleQuoted(text, indent, split)
		return
	}
	allowPlain, allowSingle := analyzeScalar(text)
	if allowPlain && !resolvesNonStr(text) {
		w.writePlain(text, indent, split)
		return
	}
	if allowSingle {
		w.writeSingleQuoted(text, indent, split)
		return
	}
	w.writeDoubleQuoted(text, indent)
}

func (w *yamlWriter) writeIndicator(indicator string, needWhitespace, whitespace, indention bool) {
	data := indicator
	if !w.whitespace && needWhitespace {
		data = " " + indicator
	}
	w.whitespace = whitespace
	w.indention = w.indention && indention
	w.column += utf8.RuneCountInString(data)
	w.buf.WriteString(data)
}

func (w *yamlWriter) writeLineBreak() {
	w.whitespace = true
	w.indention = true
	w.column = 0
	w.buf.WriteByte('\n')
}

func (w *yamlWriter) writeIndent(indent int) {
	if !w.indention || w.column > indent || (w.column == indent && !w.whitespace) {
		w.writeLineBreak()
	}
	if w.column < indent {
		w.whitespace = true
		w.buf.WriteString(strings.Repeat(" ", indent-w.column))
		w.column = indent
	}
}

func (w *yamlWriter) writePlain(text string, indent int, split bool) {
	if text == "" {
		return
	}
	if !w.whitespace {
		w.buf.WriteByte(' ')
		w.column++
	}
	w.whitespace = false
	w.indention = false
	runes := []rune(text)
	spaces := false
	start, end := 0, 0
	for end <= len(runes) {
		var ch rune
		has := end < len(runes)
		if has {
			ch = runes[end]
		}
		switch {
		case spaces:
			if !has || ch != ' ' {
				if start+1 == end && w.column > w.bestWidth && split {
					w.writeIndent(indent)
					w.whitespace = false
					w.indention = false
				} else {
					w.buf.WriteString(string(runes[start:end]))
					w.column += end - start
				}
				start = end
			}
		case has && ch == '\n':
			// Plain scalars never contain line breaks (analyze forbids them).
			w.buf.WriteString(string(runes[start:end]))
			w.column += end - start
			w.writeLineBreak()
			start = end
		default:
			if !has || ch == ' ' {
				w.buf.WriteString(string(runes[start:end]))
				w.column += end - start
				start = end
			}
		}
		if has {
			spaces = ch == ' '
		}
		end++
	}
}

func (w *yamlWriter) writeSingleQuoted(text string, indent int, split bool) {
	w.writeIndicator("'", true, false, false)
	runes := []rune(text)
	spaces := false
	start, end := 0, 0
	for end <= len(runes) {
		var ch rune
		has := end < len(runes)
		if has {
			ch = runes[end]
		}
		if spaces {
			if !has || ch != ' ' {
				if start+1 == end && w.column > w.bestWidth && split &&
					start != 0 && end != len(runes) {
					w.writeIndent(indent)
				} else {
					w.buf.WriteString(string(runes[start:end]))
					w.column += end - start
				}
				start = end
			}
		} else if !has || ch == ' ' || ch == '\n' || ch == '\'' {
			if start < end {
				w.buf.WriteString(string(runes[start:end]))
				w.column += end - start
				start = end
			}
		}
		if has && ch == '\'' {
			w.buf.WriteString("''")
			w.column += 2
			start = end + 1
		}
		if has {
			spaces = ch == ' '
		}
		end++
	}
	w.writeIndicator("'", false, false, false)
}

func (w *yamlWriter) writeDoubleQuoted(text string, indent int) {
	_ = indent
	w.writeIndicator("\"", true, false, false)
	for _, r := range text {
		switch r {
		case '\\':
			w.buf.WriteString(`\\`)
		case '"':
			w.buf.WriteString(`\"`)
		case '\n':
			w.buf.WriteString(`\n`)
		case '\t':
			w.buf.WriteString(`\t`)
		case '\r':
			w.buf.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&w.buf, "\\x%02x", r)
				continue
			}
			w.buf.WriteRune(r)
		}
	}
	w.writeIndicator("\"", false, false, false)
}

// analyzeScalar reports whether PyYAML may emit the string as a block plain or
// single-quoted scalar (ported from yaml/emitter.py's analyze_scalar).
func analyzeScalar(text string) (allowBlockPlain bool, allowSingle bool) {
	runes := []rune(text)
	if len(runes) == 0 {
		return false, true
	}
	blockIndicators := false
	lineBreaks := false
	special := false
	leadingSpace, leadingBreak, trailingSpace, trailingBreak := false, false, false, false
	breakSpace, spaceBreak := false, false
	precededByWhitespace := true
	followedByWhitespace := len(runes) == 1 || isYAMLWhitespace(runes[1])
	previousSpace, previousBreak := false, false

	for index := 0; index < len(runes); index++ {
		ch := runes[index]
		if index == 0 {
			if strings.ContainsRune("#,[]{}&*!|>'\"%@`", ch) {
				blockIndicators = true
			}
			if ch == '?' || ch == ':' {
				if followedByWhitespace {
					blockIndicators = true
				}
			}
			if ch == '-' && followedByWhitespace {
				blockIndicators = true
			}
		} else {
			if ch == ':' && followedByWhitespace {
				blockIndicators = true
			}
			if ch == '#' && precededByWhitespace {
				blockIndicators = true
			}
		}

		if ch == '\n' {
			lineBreaks = true
		}
		if !(ch == '\n' || (ch >= 0x20 && ch <= 0x7e)) && !isPrintableUnicode(ch) {
			special = true
		}

		switch {
		case ch == ' ':
			if index == 0 {
				leadingSpace = true
			}
			if index == len(runes)-1 {
				trailingSpace = true
			}
			if previousBreak {
				breakSpace = true
			}
			previousSpace, previousBreak = true, false
		case ch == '\n':
			if index == 0 {
				leadingBreak = true
			}
			if index == len(runes)-1 {
				trailingBreak = true
			}
			if previousSpace {
				spaceBreak = true
			}
			previousSpace, previousBreak = false, true
		default:
			previousSpace, previousBreak = false, false
		}

		precededByWhitespace = isYAMLWhitespace(ch)
		// PyYAML's off-by-one: after incrementing, it inspects scalar[index+2].
		followedByWhitespace = index+2 >= len(runes) || isYAMLWhitespace(runes[index+2])
	}

	allowBlockPlain = true
	allowSingle = true
	if leadingSpace || leadingBreak || trailingSpace || trailingBreak {
		allowBlockPlain = false
	}
	if breakSpace {
		allowBlockPlain = false
		allowSingle = false
	}
	if spaceBreak || special {
		allowBlockPlain = false
		allowSingle = false
	}
	if lineBreaks {
		allowBlockPlain = false
	}
	if blockIndicators {
		allowBlockPlain = false
	}
	return allowBlockPlain, allowSingle
}

func isYAMLWhitespace(ch rune) bool {
	switch ch {
	case 0x00, ' ', '\t', '\r', '\n', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

func isPrintableUnicode(ch rune) bool {
	if ch == 0x85 {
		return true
	}
	if ch == 0xFEFF {
		return false
	}
	return (ch >= 0xA0 && ch <= 0xD7FF) || (ch >= 0xE000 && ch <= 0xFFFD) ||
		(ch >= 0x10000 && ch < 0x10FFFF)
}

var (
	boolScalar  = regexp.MustCompile(`^(y|Y|yes|Yes|YES|n|N|no|No|NO|true|True|TRUE|false|False|FALSE|on|On|ON|off|Off|OFF)$`)
	intScalar   = regexp.MustCompile(`^[-+]?(0b[0-1_]+|0x[0-9a-fA-F_]+|0[0-7_]+|[0-9][0-9_]*)$`)
	floatScalar = regexp.MustCompile(
		`^[-+]?((([0-9][0-9_]*)?\.[0-9_]+|[0-9][0-9_]*)([eE][-+]?[0-9]+)?|\.(inf|Inf|INF|nan|NaN|NAN))$`)
	dateScalar = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}([Tt ].*)?$`)
)

// resolvesNonStr reports whether a plain string would be resolved by PyYAML's
// implicit resolvers to a non-string tag, forcing the quotes.
func resolvesNonStr(text string) bool {
	switch text {
	case "", "~", "null", "Null", "NULL":
		return true
	}
	return boolScalar.MatchString(text) || intScalar.MatchString(text) ||
		floatScalar.MatchString(text) || dateScalar.MatchString(text)
}

func stringsToAny(items []string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

// nodeValue converts a catalog YAML node into the dumper's value tree.
func nodeValue(node *yaml.Node) any {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) > 0 {
			return nodeValue(node.Content[0])
		}
		return nil
	case yaml.AliasNode:
		return nodeValue(node.Alias)
	case yaml.MappingNode:
		mapping := render.Ordered{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			mapping.Set(node.Content[i].Value, nodeValue(node.Content[i+1]))
		}
		return mapping
	case yaml.SequenceNode:
		seq := make([]any, 0, len(node.Content))
		for _, item := range node.Content {
			seq = append(seq, nodeValue(item))
		}
		return seq
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!bool":
			var b bool
			if err := node.Decode(&b); err == nil {
				return b
			}
		case "!!int":
			var i int64
			if err := node.Decode(&i); err == nil {
				return int(i)
			}
		case "!!float":
			var f float64
			if err := node.Decode(&f); err == nil {
				return f
			}
		case "!!null":
			return nil
		}
		return node.Value
	}
	return nil
}
