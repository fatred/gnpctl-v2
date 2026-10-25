package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// rpcTimeout bounds every RPC and the Subscribe stream's Recv so a device that
// accepts the connection but never answers becomes a per-target error, not a
// hang. A var (not a const) only so the tests can shorten it.
var rpcTimeout = 30 * time.Second

// LiveTransport is the gRPC gNMI transport.
//
// Policy: never plaintext, and validate TLS by default. Devices serve gNMI
// over TLS with proper certs (IP-SANs, so IP targets validate without an FQDN),
// so the channel is TLS and the server cert is verified. When the issuing CA is
// not in the host trust store, point at the CA bundle via TLSCA rather than
// disabling verification.
type LiveTransport struct {
	Host     string
	Port     int
	Username string
	Password string
	TLSCA    string

	conn   *grpc.ClientConn
	client gnmi.GNMIClient
}

var _ GnmiTransport = (*LiveTransport)(nil)

// NewLiveTransport validates the TLS policy and prepares a lazy connection.
// Port 0 means DefaultGNMIPort. No RPC is made until the first read.
func NewLiveTransport(host string, port int, username, password string, tlsCA string) (*LiveTransport, error) {
	return &LiveTransport{Host: host, Port: port, Username: username, Password: password, TLSCA: tlsCA}, nil
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
	cfg := &tls.Config{}
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
	if t.Username != "" || t.Password != "" {
		// pygnmi always sends both keys; send both whenever either is configured.
		ctx = metadata.AppendToOutgoingContext(ctx, "username", t.Username, "password", t.Password)
	}
	return ctx
}

// rpcContext is one RPC's context: the credential metadata plus the deadline.
func (t *LiveTransport) rpcContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(t.context(), rpcTimeout)
}

func (t *LiveTransport) grpcError(err error) error {
	return fmt.Errorf("GRPC ERROR Host: %s, Error: %v", t.addr(), err)
}

func (t *LiveTransport) Capabilities() (map[string]any, error) {
	client, err := t.connect()
	if err != nil {
		return nil, t.grpcError(err)
	}
	ctx, cancel := t.rpcContext()
	defer cancel()
	resp, err := client.Capabilities(ctx, &gnmi.CapabilityRequest{})
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
			names = append(names, encodingName(e))
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
	enc, err := encodingValue(encoding)
	if err != nil {
		return nil, err
	}
	client, err := t.connect()
	if err != nil {
		return nil, t.grpcError(err)
	}
	ctx, cancel := t.rpcContext()
	defer cancel()
	subs := make([]*gnmi.Subscription, 0, len(paths))
	for _, p := range paths {
		subs = append(subs, &gnmi.Subscription{Path: parsePath(p)})
	}
	req := &gnmi.SubscribeRequest{
		Request: &gnmi.SubscribeRequest_Subscribe{
			Subscribe: &gnmi.SubscriptionList{
				Subscription: subs,
				Mode:         gnmi.SubscriptionList_ONCE,
				Encoding:     enc,
			},
		},
	}

	stream, err := client.Subscribe(ctx)
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

// encodingName reverses a gNMI encoding enum the way pygnmi does: every value
// above ASCII (3) is reported as json_ietf.
func encodingName(e gnmi.Encoding) string {
	switch e {
	case gnmi.Encoding_JSON:
		return "json"
	case gnmi.Encoding_BYTES:
		return "bytes"
	case gnmi.Encoding_PROTO:
		return "proto"
	case gnmi.Encoding_ASCII:
		return "ascii"
	default:
		return "json_ietf"
	}
}

// encodingValue maps a gNMI encoding name to its enum. An empty name is
// json_ietf, pygnmi's default; an unknown name is an error, never silent JSON.
func encodingValue(name string) (gnmi.Encoding, error) {
	switch strings.ToLower(name) {
	case "", "json_ietf":
		return gnmi.Encoding_JSON_IETF, nil
	case "json":
		return gnmi.Encoding_JSON, nil
	case "bytes":
		return gnmi.Encoding_BYTES, nil
	case "proto":
		return gnmi.Encoding_PROTO, nil
	case "ascii":
		return gnmi.Encoding_ASCII, nil
	default:
		return 0, fmt.Errorf("'%s' is not a valid Encoding", strings.ToUpper(name))
	}
}

// pathKeySelector matches a gNMI key selector; extracting these before
// splitting on "/" keeps values containing "/" intact.
var pathKeySelector = regexp.MustCompile(`\[[^\[\]]*=[^\[\]]*\]`)

type pathKey struct {
	key   string
	value string
}

// parsePath converts an XPath-like string to a gNMI Path the way pygnmi's
// gnmi_path_generator does: key selectors are lifted out first, the origin comes
// only from the first element, and a bare colon in later elements stays part of
// the element name.
func parsePath(in string) *gnmi.Path {
	var keys []pathKey
	protected := pathKeySelector.ReplaceAllStringFunc(in, func(selector string) string {
		key, value, _ := strings.Cut(selector[1:len(selector)-1], "=")
		keys = append(keys, pathKey{key: key, value: value})
		return fmt.Sprintf("[%d]", len(keys)-1)
	})
	path := &gnmi.Path{}
	first := true
	for _, element := range strings.Split(protected, "/") {
		if element == "" {
			continue
		}
		if first {
			first = false
			if origin, rest, ok := strings.Cut(element, ":"); ok && origin != "" {
				path.Origin = origin
				if rest == "" {
					continue
				}
				element = rest
			}
		}
		path.Elem = append(path.Elem, parseElem(element, keys))
	}
	return path
}

func parseElem(element string, keys []pathKey) *gnmi.PathElem {
	name, rest, ok := strings.Cut(element, "[")
	if !ok {
		return &gnmi.PathElem{Name: element}
	}
	elem := &gnmi.PathElem{Name: name}
	for rest != "" {
		part, after, ok := strings.Cut(rest, "]")
		if !ok {
			return &gnmi.PathElem{Name: element} // not a selector: pygnmi keeps the element whole
		}
		index, err := strconv.Atoi(part)
		if err != nil || index < 0 || index >= len(keys) {
			return &gnmi.PathElem{Name: element}
		}
		if elem.Key == nil {
			elem.Key = map[string]string{}
		}
		elem.Key[keys[index].key] = keys[index].value
		rest = strings.TrimPrefix(after, "[")
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
