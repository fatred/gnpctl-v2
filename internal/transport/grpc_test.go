package transport

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

type stubServer struct {
	gnmi.UnimplementedGNMIServer

	mu       sync.Mutex
	username string
	password string
	authKeys map[string]bool
	mode     gnmi.SubscriptionList_Mode
	encoding gnmi.Encoding
	path     *gnmi.Path

	encodings []gnmi.Encoding // overrides the default capability list
}

func (s *stubServer) Capabilities(ctx context.Context, _ *gnmi.CapabilityRequest) (*gnmi.CapabilityResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	s.username = first(md.Get("username"))
	s.password = first(md.Get("password"))
	s.authKeys = map[string]bool{}
	for _, key := range []string{"username", "password"} {
		_, s.authKeys[key] = md[key]
	}
	encodings := s.encodings
	s.mu.Unlock()
	if encodings == nil {
		encodings = []gnmi.Encoding{gnmi.Encoding_JSON_IETF, gnmi.Encoding_PROTO}
	}
	return &gnmi.CapabilityResponse{
		SupportedModels: []*gnmi.ModelData{
			{Name: "srl_nokia-system", Organization: "Nokia", Version: "2023-10-31"},
		},
		SupportedEncodings: encodings,
		GNMIVersion:        "0.10.0",
	}, nil
}

func (s *stubServer) Subscribe(stream grpc.BidiStreamingServer[gnmi.SubscribeRequest, gnmi.SubscribeResponse]) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	list := req.GetSubscribe()
	s.mu.Lock()
	s.mode = list.GetMode()
	s.encoding = list.GetEncoding()
	if len(list.GetSubscription()) > 0 {
		s.path = list.GetSubscription()[0].GetPath()
	}
	s.mu.Unlock()

	notification := &gnmi.Notification{
		Prefix: &gnmi.Path{Elem: []*gnmi.PathElem{{Name: "system"}}},
		Update: []*gnmi.Update{{
			Path: &gnmi.Path{Elem: []*gnmi.PathElem{{Name: "information"}, {Name: "version"}}},
			Val: &gnmi.TypedValue{
				Value: &gnmi.TypedValue_StringVal{StringVal: "v24.10.4"},
			},
		}},
	}
	if err := stream.Send(&gnmi.SubscribeResponse{
		Response: &gnmi.SubscribeResponse_Update{Update: notification},
	}); err != nil {
		return err
	}
	return stream.Send(&gnmi.SubscribeResponse{
		Response: &gnmi.SubscribeResponse_SyncResponse{SyncResponse: true},
	})
}

func first(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

// startStub serves gNMI over TLS with a self-signed cert, returning the target
// address and the CA bundle path the transport must be pointed at.
func startStub(t *testing.T, stub gnmi.GNMIServer) (host string, port int, caPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "gnpctl stub"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	caPath = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})))
	gnmi.RegisterGNMIServer(server, stub)
	go func() {
		if err := server.Serve(listener); err != nil {
			t.Logf("stub server stopped: %v", err)
		}
	}()
	t.Cleanup(server.Stop)

	addr := listener.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, caPath
}

func TestLiveCapabilitiesAndSubscribeOnce(t *testing.T) {
	stub := &stubServer{}
	host, port, caPath := startStub(t, stub)

	tr, err := NewLiveTransport(host, port, "admin", "secret", false, caPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	caps, err := tr.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	wantCaps := map[string]any{
		"supported_models": []any{map[string]any{
			"name":         "srl_nokia-system",
			"organization": "Nokia",
			"version":      "2023-10-31",
		}},
		"supported_encodings": []any{"json_ietf", "proto"},
		"gnmi_version":        "0.10.0",
	}
	if !reflect.DeepEqual(caps, wantCaps) {
		t.Errorf("capabilities = %#v, want %#v", caps, wantCaps)
	}

	stub.mu.Lock()
	username, password := stub.username, stub.password
	stub.mu.Unlock()
	if username != "admin" || password != "secret" {
		t.Errorf("metadata username/password = %q/%q, want admin/secret", username, password)
	}

	updates, err := tr.SubscribeOnce([]string{"eos_native:/Eos/image"}, "json_ietf")
	if err != nil {
		t.Fatal(err)
	}
	wantUpdates := []LeafUpdate{{Path: "system/information/version", Value: "v24.10.4"}}
	if !reflect.DeepEqual(updates, wantUpdates) {
		t.Errorf("updates = %#v, want %#v", updates, wantUpdates)
	}

	stub.mu.Lock()
	mode, encoding, path := stub.mode, stub.encoding, stub.path
	stub.mu.Unlock()
	if mode != gnmi.SubscriptionList_ONCE {
		t.Errorf("subscription mode = %v, want ONCE", mode)
	}
	if encoding != gnmi.Encoding_JSON_IETF {
		t.Errorf("encoding = %v, want JSON_IETF", encoding)
	}
	if path.GetOrigin() != "eos_native" || pathString(path) != "Eos/image" {
		t.Errorf("path = origin %q, %q; want origin eos_native, Eos/image", path.GetOrigin(), pathString(path))
	}

	// pygnmi defaults an unset encoding to json_ietf, not JSON.
	if _, err := tr.SubscribeOnce([]string{"/system/information"}, ""); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	encoding = stub.encoding
	stub.mu.Unlock()
	if encoding != gnmi.Encoding_JSON_IETF {
		t.Errorf("encoding = %v, want JSON_IETF for an empty encoding", encoding)
	}

	// A typo is an error, never a silent fallback to JSON.
	if _, err := tr.SubscribeOnce([]string{"/system/information"}, "xml"); err == nil ||
		!strings.Contains(err.Error(), "is not a valid Encoding") {
		t.Errorf("unknown encoding error = %v", err)
	}
}

func TestParsePathMatchesPygnmi(t *testing.T) {
	cases := []struct{ in, origin, want string }{
		{"/interfaces/interface[name=ethernet-1/1]/state", "", "interfaces/interface[name=ethernet-1/1]/state"},
		{"rfc7951:/openconfig-system:system/state", "rfc7951", "openconfig-system:system/state"},
		{"eos_native:/Eos/image", "eos_native", "Eos/image"},
		{"/origin:cont/leaf", "origin", "cont/leaf"},
		{"origin://", "origin", ""},
		{"/interfaces/interface[name=eth0][type=phy]/state", "", "interfaces/interface[name=eth0][type=phy]/state"},
		{"", "", ""},
	}
	for _, c := range cases {
		path := parsePath(c.in)
		if path.GetOrigin() != c.origin || pathString(path) != c.want {
			t.Errorf("parsePath(%q) = origin %q, %q; want origin %q, %q",
				c.in, path.GetOrigin(), pathString(path), c.origin, c.want)
		}
	}
}

func TestCapabilitiesEncodingsMatchPygnmi(t *testing.T) {
	stub := &stubServer{encodings: []gnmi.Encoding{0, 1, 2, 3, 4, 5}}
	host, port, caPath := startStub(t, stub)
	tr, err := NewLiveTransport(host, port, "", "", false, caPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	caps, err := tr.Capabilities()
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"json", "bytes", "proto", "ascii", "json_ietf", "json_ietf"}
	if !reflect.DeepEqual(caps["supported_encodings"], want) {
		t.Errorf("encodings = %v, want %v", caps["supported_encodings"], want)
	}
}

func TestEitherCredentialSendsBothMetadataKeys(t *testing.T) {
	stub := &stubServer{}
	host, port, caPath := startStub(t, stub)
	tr, err := NewLiveTransport(host, port, "admin", "", false, caPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	if _, err := tr.Capabilities(); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	username, password, keys := stub.username, stub.password, stub.authKeys
	stub.mu.Unlock()
	if username != "admin" || password != "" {
		t.Errorf("metadata = %q/%q, want admin/\"\"", username, password)
	}
	if !keys["username"] || !keys["password"] {
		t.Errorf("metadata keys sent = %v, want both username and password", keys)
	}
}

func TestUntrustedServerCertificateIsRejected(t *testing.T) {
	host, port, _ := startStub(t, &stubServer{})
	_, _, otherCA := startStub(t, &stubServer{})
	tr, err := NewLiveTransport(host, port, "", "", false, otherCA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	if _, err := tr.Capabilities(); err == nil {
		t.Fatal("Capabilities succeeded against a server whose certificate is not trusted")
	}
}

// hungServer accepts every RPC and never answers; the transport must bound it
// with a deadline instead of blocking forever.
type hungServer struct {
	gnmi.UnimplementedGNMIServer
}

func (s *hungServer) Capabilities(ctx context.Context, _ *gnmi.CapabilityRequest) (*gnmi.CapabilityResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *hungServer) Subscribe(stream grpc.BidiStreamingServer[gnmi.SubscribeRequest, gnmi.SubscribeResponse]) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	<-stream.Context().Done()
	return stream.Context().Err()
}

func TestEveryRPCHasADeadline(t *testing.T) {
	previous := rpcTimeout
	rpcTimeout = 200 * time.Millisecond
	t.Cleanup(func() { rpcTimeout = previous })

	host, port, caPath := startStub(t, &hungServer{})
	tr, err := NewLiveTransport(host, port, "", "", false, caPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tr.Close() })

	for name, call := range map[string]func() error{
		"Capabilities": func() error { _, err := tr.Capabilities(); return err },
		"SubscribeOnce": func() error {
			_, err := tr.SubscribeOnce([]string{"/interfaces"}, "json_ietf")
			return err
		},
	} {
		start := time.Now()
		if err := call(); err == nil {
			t.Fatalf("%s succeeded against a server that never answers", name)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("%s took %v, want the %v deadline to fire", name, elapsed, rpcTimeout)
		}
	}
}
