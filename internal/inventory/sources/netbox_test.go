package sources

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const recordedNetBox = `{
  "data": {
    "device_list": [
      {
        "name": "leaf1",
        "primary_ip4": {"address": "10.0.0.1/24"},
        "platform": {"slug": "nokia_srl"},
        "services": [
          {"name": "gnmi", "ports": [57400], "ipaddresses": []}
        ]
      },
      {
        "name": "leaf2",
        "primary_ip4": {"address": "10.0.0.2/24"},
        "platform": {"slug": "arista_eos"},
        "services": []
      }
    ]
  }
}`

type capturedRequest struct {
	path string
	auth string
	body map[string]any
}

func netboxServer(t *testing.T, captured *capturedRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.path = r.URL.Path
		captured.auth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&captured.body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = w.Write([]byte(recordedNetBox))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func fetchNetBox(t *testing.T, params map[string]string, credentialRef string) []Target {
	t.Helper()
	targets, err := (&NetBoxSource{Params: params, CredentialRef: credentialRef}).Fetch()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	return targets
}

func TestNetBoxTagScopedGraphQLPullAndPortFromService(t *testing.T) {
	t.Setenv("NETBOX_TOKEN", "secret-tok")
	captured := &capturedRequest{}
	srv := netboxServer(t, captured)
	targets := byName(fetchNetBox(t, map[string]string{"url": srv.URL, "tag": "gnmi-managed"}, "NETBOX_TOKEN"))

	if captured.path != "/graphql/" {
		t.Fatalf("path = %q, want /graphql/", captured.path)
	}
	// NetBox 4.x filter shape: tags by slug in_list (not the old graphene tag: arg).
	if !strings.Contains(captured.body["query"].(string), "tags: {slug: {in_list: $tag}}") {
		t.Fatalf("query = %v", captured.body["query"])
	}
	tags, _ := captured.body["variables"].(map[string]any)["tag"].([]any)
	if len(tags) != 1 || tags[0] != "gnmi-managed" {
		t.Fatalf("variables = %v", captured.body["variables"])
	}
	if captured.auth != "Token secret-tok" {
		t.Fatalf("auth = %q", captured.auth)
	}

	leaf1 := targets["leaf1"]
	if leaf1.Port == nil || *leaf1.Port != 57400 {
		t.Fatalf("leaf1 port = %v", leaf1.Port)
	}
	if leaf1.Address == nil || *leaf1.Address != "10.0.0.1" {
		t.Fatalf("leaf1 address = %v", leaf1.Address)
	}
	if leaf1.PlatformHint == nil || *leaf1.PlatformHint != "nokia_srl" {
		t.Fatalf("leaf1 platform = %v", leaf1.PlatformHint)
	}
}

func TestNetBoxDeviceWithoutGNMIServiceHasUnsetPort(t *testing.T) {
	t.Setenv("NETBOX_TOKEN", "tok")
	srv := netboxServer(t, &capturedRequest{})
	targets := byName(fetchNetBox(t, map[string]string{"url": srv.URL, "tag": "gnmi-managed"}, "NETBOX_TOKEN"))
	leaf2 := targets["leaf2"]
	if leaf2.Port != nil {
		t.Fatalf("leaf2 port = %v, want unset", *leaf2.Port)
	}
	if leaf2.Address == nil || *leaf2.Address != "10.0.0.2" {
		t.Fatalf("leaf2 address = %v", leaf2.Address)
	}
}

func TestNetBoxServiceAddressAndCustomServiceName(t *testing.T) {
	t.Setenv("NETBOX_TOKEN", "tok")
	recording := `{"data": {"device_list": [{
	  "name": "r1",
	  "primary_ip4": {"address": "10.0.0.1/24"},
	  "platform": {"slug": "nokia_srl"},
	  "services": [{"name": "telemetry", "ports": [6030], "ipaddresses": [{"address": "10.9.9.9/32"}]}]
	}]}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(recording))
	}))
	defer srv.Close()
	targets := byName(fetchNetBox(t, map[string]string{
		"url": srv.URL, "tag": "t", "service_name": "telemetry",
	}, "NETBOX_TOKEN"))
	r1 := targets["r1"]
	if r1.Port == nil || *r1.Port != 6030 {
		t.Fatalf("r1 port = %v", r1.Port)
	}
	if r1.Address == nil || *r1.Address != "10.9.9.9" {
		t.Fatalf("r1 address = %v", r1.Address)
	}
}

func TestNetBoxGraphQLErrorsSurface(t *testing.T) {
	t.Setenv("NETBOX_TOKEN", "tok")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": null, "errors": [{"message": "bad filter"}]}`))
	}))
	defer srv.Close()
	_, err := (&NetBoxSource{
		Params:        map[string]string{"url": srv.URL, "tag": "t"},
		CredentialRef: "NETBOX_TOKEN",
	}).Fetch()
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *SourceError", err)
	}
	if sourceErr.Msg != "netbox GraphQL error: bad filter" {
		t.Fatalf("msg = %q", sourceErr.Msg)
	}
}

func TestNetBoxValidateRejectsNonHTTPS(t *testing.T) {
	err := ValidateNetBoxParams(map[string]string{"url": "http://nb", "tag": "t"}, "NB")
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *SourceError", err)
	}
	if !strings.Contains(strings.ToLower(sourceErr.Msg), "https") {
		t.Fatalf("msg = %q", sourceErr.Msg)
	}
}

func TestNetBoxValidateAcceptsHTTPS(t *testing.T) {
	if err := ValidateNetBoxParams(map[string]string{"url": "https://nb", "tag": "t"}, "NB"); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestNetBoxMissingTokenEnvErrors(t *testing.T) {
	t.Setenv("NETBOX_TOKEN", "")
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()
	_, err := (&NetBoxSource{
		Params:        map[string]string{"url": srv.URL, "tag": "t"},
		CredentialRef: "NETBOX_TOKEN",
	}).Fetch()
	var sourceErr *SourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("err = %v, want *SourceError", err)
	}
	if !strings.Contains(sourceErr.Msg, "NETBOX_TOKEN") {
		t.Fatalf("msg = %q", sourceErr.Msg)
	}
	if called {
		t.Fatal("request made without a token")
	}
}
