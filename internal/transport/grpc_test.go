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
	mode     gnmi.SubscriptionList_Mode
	encoding gnmi.Encoding
	path     *gnmi.Path
}

func (s *stubServer) Capabilities(ctx context.Context, _ *gnmi.CapabilityRequest) (*gnmi.CapabilityResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	s.username = first(md.Get("username"))
	s.password = first(md.Get("password"))
	s.mu.Unlock()
	return &gnmi.CapabilityResponse{
		SupportedModels: []*gnmi.ModelData{
			{Name: "srl_nokia-system", Organization: "Nokia", Version: "2023-10-31"},
		},
		SupportedEncodings: []gnmi.Encoding{gnmi.Encoding_JSON_IETF, gnmi.Encoding_PROTO},
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
func startStub(t *testing.T, stub *stubServer) (host string, port int, caPath string) {
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

	// The transport applies no encoding default of its own: empty is the proto
	// zero value, JSON.
	if _, err := tr.SubscribeOnce([]string{"/system/information"}, ""); err != nil {
		t.Fatal(err)
	}
	stub.mu.Lock()
	encoding = stub.encoding
	stub.mu.Unlock()
	if encoding != gnmi.Encoding_JSON {
		t.Errorf("encoding = %v, want JSON for an empty encoding", encoding)
	}
}
