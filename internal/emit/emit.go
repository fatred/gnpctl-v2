// Package emit serializes a DesiredConfig to the gnmic YAML files. It builds a
// yaml.Node tree in Python's key order and encodes it with yaml.v3, so the files
// are semantically identical to PyYAML's safe_dump(sort_keys=False) output.
package emit

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// dump encodes one artifact as a YAML document with a two-space indent.
func dump(doc render.Ordered) (string, error) {
	root, err := mappingNode(doc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// mappingNode builds a mapping node, preserving the Ordered key order.
func mappingNode(doc render.Ordered) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i, key := range doc.Keys {
		value, err := valueNode(doc.Values[i])
		if err != nil {
			return nil, err
		}
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
	}
	return node, nil
}

func valueNode(value any) (*yaml.Node, error) {
	switch v := value.(type) {
	case *yaml.Node:
		return copyNode(v), nil
	case render.Ordered:
		return mappingNode(v)
	case *render.Ordered:
		if v == nil {
			return nullNode(), nil
		}
		return mappingNode(*v)
	case []any:
		return sequenceNode(v)
	case []string:
		items := make([]any, len(v))
		for i, item := range v {
			items[i] = item
		}
		return sequenceNode(items)
	case nil:
		return nullNode(), nil
	case bool:
		return scalarNode("!!bool", strconv.FormatBool(v)), nil
	case int:
		return scalarNode("!!int", strconv.Itoa(v)), nil
	case int64:
		return scalarNode("!!int", strconv.FormatInt(v, 10)), nil
	case float64:
		return scalarNode("!!float", floatText(v)), nil
	case string:
		return scalarNode("!!str", v), nil
	default:
		return nil, fmt.Errorf("yaml: cannot dump %T", value)
	}
}

func sequenceNode(items []any) (*yaml.Node, error) {
	node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, item := range items {
		child, err := valueNode(item)
		if err != nil {
			return nil, err
		}
		node.Content = append(node.Content, child)
	}
	return node, nil
}

func scalarNode(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func nullNode() *yaml.Node { return scalarNode("!!null", "null") }

// floatText formats a float so YAML readers resolve it as a float: PyYAML-style
// .inf/.nan and a trailing .0 when the shortest form would look like an int.
func floatText(f float64) string {
	switch {
	case math.IsInf(f, 1):
		return ".inf"
	case math.IsInf(f, -1):
		return "-.inf"
	case math.IsNaN(f):
		return ".nan"
	}
	text := strconv.FormatFloat(f, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

// copyNode deep-copies a catalog YAML node as a plain block node, resolving
// aliases so the emitted document never references an anchor outside itself.
func copyNode(node *yaml.Node) *yaml.Node {
	switch node.Kind {
	case yaml.DocumentNode:
		if len(node.Content) > 0 {
			return copyNode(node.Content[0])
		}
		return nullNode()
	case yaml.AliasNode:
		return copyNode(node.Alias)
	case yaml.MappingNode:
		copied := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for i := 0; i+1 < len(node.Content); i += 2 {
			copied.Content = append(copied.Content,
				copyNode(node.Content[i]), copyNode(node.Content[i+1]))
		}
		return copied
	case yaml.SequenceNode:
		copied := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range node.Content {
			copied.Content = append(copied.Content, copyNode(item))
		}
		return copied
	default:
		tag := node.Tag
		if tag == "" {
			tag = "!!str"
		}
		return scalarNode(tag, node.Value)
	}
}

