package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// LiveTransport is the gRPC gNMI transport.
//
// Policy: never plaintext, and validate TLS by default. Devices serve gNMI
// over TLS with proper certs (IP-SANs, so IP targets validate without an FQDN),
// so the channel is TLS and the server cert is verified. When the issuing CA is
// not in the host trust store, point at the CA bundle via TLSCA rather than
// disabling verification.
type LiveTransport struct {
	Host       string
	Port       int
	Username   string
	Password   string
	SkipVerify bool
	TLSCA      string

	conn   *grpc.ClientConn
	client gnmi.GNMIClient
}

var _ GnmiTransport = (*LiveTransport)(nil)

// NewLiveTransport validates the TLS policy and prepares a lazy connection.
// Port 0 means DefaultGNMIPort. No RPC is made until the first read.
func NewLiveTransport(host string, port int, username, password string, skipVerify bool, tlsCA string) (*LiveTransport, error) {
	// A CA bundle is the escape hatch used INSTEAD of disabling verification —
	// never both. The path of least resistance for "my CA isn't trusted" must
	// be tlsCA.
	if skipVerify && tlsCA != "" {
		return nil, errors.New("skip_verify cannot be combined with tls_ca — use tls_ca to trust a private CA (the secure path), not skip_verify")
	}
	if port == 0 {
		port = DefaultGNMIPort
	}
	if skipVerify {
		slog.Warn(fmt.Sprintf("gNMI TLS server-certificate verification DISABLED (skip_verify) for %s", net.JoinHostPort(host, strconv.Itoa(port))))
	}
	return &LiveTransport{
		Host:       host,
		Port:       port,
		Username:   username,
		Password:   password,
		SkipVerify: skipVerify,
		TLSCA:      tlsCA,
	}, nil
}

// Close releases the connection.
func (t *LiveTransport) Close() error {
	if t.conn == nil {
		return nil
	}
	conn := t.conn
	t.conn, t.client = nil, nil
	return conn.Close()
}

func (t *LiveTransport) addr() string {
	return net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
}

func (t *LiveTransport) connect() (gnmi.GNMIClient, error) {
	if t.client != nil {
		return t.client, nil
	}
	creds, err := t.credentials()
	if err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient(t.addr(), grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, err
	}
	t.conn, t.client = conn, gnmi.NewGNMIClient(conn)
	return t.client, nil
}

func (t *LiveTransport) credentials() (credentials.TransportCredentials, error) {
	cfg := &tls.Config{InsecureSkipVerify: t.SkipVerify}
	if t.TLSCA != "" {
		pem, err := os.ReadFile(t.TLSCA)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates found in %s", t.TLSCA)
		}
		cfg.RootCAs = pool
	}
	return credentials.NewTLS(cfg), nil
}

func (t *LiveTransport) context() context.Context {
	ctx := context.Background()
	if t.Username != "" && t.Password != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "username", t.Username, "password", t.Password)
	}
	return ctx
}

func (t *LiveTransport) grpcError(err error) error {
	return fmt.Errorf("GRPC ERROR Host: %s, Error: %v", t.addr(), err)
}

func (t *LiveTransport) Capabilities() (map[string]any, error) {
	client, err := t.connect()
	if err != nil {
		return nil, t.grpcError(err)
	}
	resp, err := client.Capabilities(t.context(), &gnmi.CapabilityRequest{})
	if err != nil {
		return nil, t.grpcError(err)
	}
	caps := map[string]any{}
	if len(resp.GetSupportedModels()) > 0 {
		models := make([]any, 0, len(resp.GetSupportedModels()))
		for _, m := range resp.GetSupportedModels() {
			models = append(models, map[string]any{
				"name":         m.GetName(),
				"organization": m.GetOrganization(),
				"version":      m.GetVersion(),
			})
		}
		caps["supported_models"] = models
	}
	if encodings := resp.GetSupportedEncodings(); len(encodings) > 0 {
		names := make([]any, 0, len(encodings))
		for _, e := range encodings {
			names = append(names, strings.ToLower(e.String()))
		}
		caps["supported_encodings"] = names
	}
	if v := resp.GetGNMIVersion(); v != "" {
		caps["gnmi_version"] = v
	}
	return caps, nil
}

// SubscribeOnce subscribes in ONCE mode and flattens the notifications to leaf
// updates until the target's sync_response.
func (t *LiveTransport) SubscribeOnce(paths []string, encoding string) ([]LeafUpdate, error) {
	client, err := t.connect()
	if err != nil {
		return nil, t.grpcError(err)
	}
	subs := make([]*gnmi.Subscription, 0, len(paths))
	for _, p := range paths {
		subs = append(subs, &gnmi.Subscription{Path: parsePath(p)})
	}
	req := &gnmi.SubscribeRequest{
		Request: &gnmi.SubscribeRequest_Subscribe{
			Subscribe: &gnmi.SubscriptionList{
				Subscription: subs,
				Mode:         gnmi.SubscriptionList_ONCE,
				Encoding:     encodingValue(encoding),
			},
		},
	}

	stream, err := client.Subscribe(t.context())
	if err != nil {
		return nil, t.grpcError(err)
	}
	if err := stream.Send(req); err != nil {
		return nil, t.grpcError(err)
	}
	if err := stream.CloseSend(); err != nil {
		return nil, t.grpcError(err)
	}

	var updates []LeafUpdate
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, t.grpcError(err)
		}
		if notification := resp.GetUpdate(); notification != nil {
			prefix := pathString(notification.GetPrefix())
			for _, leaf := range notification.GetUpdate() {
				full := pathString(leaf.GetPath())
				if prefix != "" {
					full = prefix + "/" + full
				}
				updates = append(updates, LeafUpdate{Path: full, Value: typedValue(leaf.GetVal())})
			}
		}
		if resp.GetSyncResponse() {
			break
		}
	}
	return updates, nil
}

// encodingValue maps a gNMI encoding name to its enum without imposing a
// default: an empty name is the proto zero value (JSON).
func encodingValue(name string) gnmi.Encoding {
	switch strings.ToLower(name) {
	case "json":
		return gnmi.Encoding_JSON
	case "bytes":
		return gnmi.Encoding_BYTES
	case "proto":
		return gnmi.Encoding_PROTO
	case "ascii":
		return gnmi.Encoding_ASCII
	case "json_ietf":
		return gnmi.Encoding_JSON_IETF
	default:
		return gnmi.Encoding_JSON
	}
}

// parsePath converts an XPath-like string to a gNMI Path the way pygnmi does:
// a leading "origin:" on the first element sets the origin; a bare colon in
// later elements stays part of the element name; "[key=value]" selectors
// become PathElem keys.
func parsePath(in string) *gnmi.Path {
	elements := strings.Split(in, "/")
	path := &gnmi.Path{}
	for i, element := range elements {
		if element == "" {
			continue
		}
		if i == 0 || len(path.Elem) == 0 {
			if origin, rest, ok := strings.Cut(element, ":"); ok {
				path.Origin = origin
				if rest == "" {
					continue
				}
				element = rest
			}
		}
		path.Elem = append(path.Elem, parseElem(element))
	}
	return path
}

func parseElem(element string) *gnmi.PathElem {
	parts := strings.Split(element, "[")
	elem := &gnmi.PathElem{Name: parts[0]}
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(strings.TrimSuffix(part, "]"), "=")
		if !ok {
			continue
		}
		if elem.Key == nil {
			elem.Key = map[string]string{}
		}
		elem.Key[key] = value
	}
	return elem
}

// pathString renders a gNMI Path the way pygnmi's degenerator does: elements
// joined by "/", keys sorted, origin omitted.
func pathString(path *gnmi.Path) string {
	elements := make([]string, 0, len(path.GetElem()))
	for _, elem := range path.GetElem() {
		var b strings.Builder
		b.WriteString(elem.GetName())
		keys := make([]string, 0, len(elem.GetKey()))
		for key := range elem.GetKey() {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "[%s=%s]", key, elem.GetKey()[key])
		}
		elements = append(elements, b.String())
	}
	return strings.Join(elements, "/")
}

func typedValue(tv *gnmi.TypedValue) any {
	switch v := tv.GetValue().(type) {
	case *gnmi.TypedValue_StringVal:
		return v.StringVal
	case *gnmi.TypedValue_IntVal:
		return v.IntVal
	case *gnmi.TypedValue_UintVal:
		return v.UintVal
	case *gnmi.TypedValue_BoolVal:
		return v.BoolVal
	case *gnmi.TypedValue_FloatVal:
		return float64(v.FloatVal)
	case *gnmi.TypedValue_DoubleVal:
		return v.DoubleVal
	case *gnmi.TypedValue_DecimalVal:
		return v.DecimalVal
	case *gnmi.TypedValue_AnyVal:
		return v.AnyVal
	case *gnmi.TypedValue_AsciiVal:
		return v.AsciiVal
	case *gnmi.TypedValue_ProtoBytes:
		return v.ProtoBytes
	case *gnmi.TypedValue_JsonVal:
		return decodeJSON(v.JsonVal)
	case *gnmi.TypedValue_JsonIetfVal:
		return decodeJSON(v.JsonIetfVal)
	case *gnmi.TypedValue_LeaflistVal:
		var b strings.Builder
		for _, element := range v.LeaflistVal.GetElement() {
			b.WriteString(element.GetStringVal())
		}
		return b.String()
	}
	return nil
}

func decodeJSON(data []byte) any {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil
	}
	return value
}
