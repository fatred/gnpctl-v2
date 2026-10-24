package sources

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

const NetBoxType = "netbox"

var NetBoxRequiredParams = []string{"url", "tag"}

// NetBox 4.x GraphQL (strawberry-django): tags are filtered by a TagFilter
// object, `tags: {slug: {in_list: [...]}}`, not the old graphene `tag:` argument.
const graphqlQuery = `query ($tag: [String!]) {
  device_list(filters: {tags: {slug: {in_list: $tag}}}) {
    name
    primary_ip4 { address }
    platform { slug }
    services { name ports ipaddresses { address } }
  }
}`

// httpx defaults to a 5s timeout; keep pulls from hanging forever.
var netboxClient = &http.Client{Timeout: 5 * time.Second}

// NetBoxSource pulls tag-scoped devices from NetBox over GraphQL.
type NetBoxSource struct {
	Params        map[string]string
	CredentialRef string
}

func ValidateNetBoxParams(params map[string]string, credentialRef string) error {
	if err := ValidateBase(NetBoxType, NetBoxRequiredParams, true, params, credentialRef); err != nil {
		return err
	}
	url := params["url"]
	if !strings.HasPrefix(strings.ToLower(url), "https://") {
		return &SourceError{Msg: fmt.Sprintf(
			"netbox source url must use https:// (got '%s') — the API token must not be sent in cleartext",
			url,
		)}
	}
	return nil
}

func (s *NetBoxSource) token() (string, error) {
	if s.CredentialRef == "" {
		return "", &SourceError{Msg: "netbox source requires a token env-var name"}
	}
	token := os.Getenv(s.CredentialRef)
	if token == "" {
		return "", &SourceError{Msg: fmt.Sprintf("netbox token env var '%s' is not set", s.CredentialRef)}
	}
	return token, nil
}

func (s *NetBoxSource) Fetch() ([]Target, error) {
	baseURL := strings.TrimRight(s.Params["url"], "/")
	tag := s.Params["tag"]
	serviceName, ok := s.Params["service_name"]
	if !ok {
		serviceName = "gnmi"
	}
	token, err := s.token() // fail before any request if unset
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{
		"query":     graphqlQuery,
		"variables": map[string]any{"tag": []string{tag}},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/graphql/", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := netboxClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		kind := "Client error"
		if resp.StatusCode >= 500 {
			kind = "Server error"
		}
		return nil, fmt.Errorf("%s '%s' for url '%s'", kind, resp.Status, req.URL)
	}
	var payload struct {
		Data *struct {
			DeviceList []netboxDevice `json:"device_list"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	// GraphQL returns HTTP 200 even when the query itself errors; surface the
	// failure instead of silently yielding 0 targets.
	if len(payload.Errors) > 0 {
		messages := make([]string, 0, len(payload.Errors))
		for _, raw := range payload.Errors {
			var gqlErr struct {
				Message any `json:"message"`
			}
			if err := json.Unmarshal(raw, &gqlErr); err == nil && gqlErr.Message != nil {
				messages = append(messages, fmt.Sprint(gqlErr.Message))
				continue
			}
			messages = append(messages, string(raw))
		}
		return nil, &SourceError{Msg: "netbox GraphQL error: " + strings.Join(messages, "; ")}
	}
	var targets []Target
	if payload.Data != nil {
		for _, device := range payload.Data.DeviceList {
			if target, ok := mapNetBoxDevice(device, serviceName); ok {
				targets = append(targets, target)
			}
		}
	}
	return targets, nil
}

type netboxAddress struct {
	Address *string `json:"address"`
}

type netboxDevice struct {
	Name       *string        `json:"name"`
	PrimaryIP4 *netboxAddress `json:"primary_ip4"`
	Platform   *struct {
		Slug *string `json:"slug"`
	} `json:"platform"`
	Services []struct {
		Name        *string         `json:"name"`
		Ports       []int           `json:"ports"`
		IPAddresses []netboxAddress `json:"ipaddresses"`
	} `json:"services"`
}

func mapNetBoxDevice(device netboxDevice, serviceName string) (Target, bool) {
	if device.Name == nil || *device.Name == "" {
		return Target{}, false
	}
	var port *int
	var serviceAddress *string
	for _, service := range device.Services {
		if service.Name != nil && *service.Name == serviceName {
			if len(service.Ports) > 0 {
				p := service.Ports[0]
				port = &p
			}
			if len(service.IPAddresses) > 0 {
				serviceAddress = stripCIDR(service.IPAddresses[0].Address)
			}
			break
		}
	}
	address := serviceAddress
	if address == nil || *address == "" {
		address = nil
		if device.PrimaryIP4 != nil {
			address = stripCIDR(device.PrimaryIP4.Address)
		}
	}
	var platform *string
	if device.Platform != nil && device.Platform.Slug != nil {
		slug := *device.Platform.Slug
		platform = &slug
	}
	return Target{
		Name:         *device.Name,
		Address:      address,
		Port:         port,
		PlatformHint: platform,
	}, true
}

func stripCIDR(address *string) *string {
	if address == nil || *address == "" {
		return nil
	}
	stripped := strings.SplitN(*address, "/", 2)[0]
	return &stripped
}
