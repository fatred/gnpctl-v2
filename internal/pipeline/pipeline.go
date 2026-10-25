// Package pipeline is discovery: port -> capabilities -> vendor -> probe ->
// variant, plus the target selection the discover command runs it over.
package pipeline

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/fatred/gnpctl-v2/internal/capabilities"
	"github.com/fatred/gnpctl-v2/internal/catalog"
	"github.com/fatred/gnpctl-v2/internal/detail"
	"github.com/fatred/gnpctl-v2/internal/fingerprint"
	"github.com/fatred/gnpctl-v2/internal/state"
	"github.com/fatred/gnpctl-v2/internal/transport"
)

// TransportFactory builds the transport for one target on its resolved port.
// A non-nil error is a per-target connection failure (recorded, run continues).
type TransportFactory func(target map[string]any, port int) (transport.GnmiTransport, error)

// DefaultTransportFactory builds a live transport, sourcing gNMI credentials
// from the environment. Secrets stay there: the device account is TARGET_USER /
// TARGET_PASS; GNMI_TLS_CA is the CA bundle used instead of disabling
// verification.
var DefaultTransportFactory TransportFactory = liveTransport

func liveTransport(target map[string]any, port int) (transport.GnmiTransport, error) {
	host := stringField(target, "address")
	if host == "" {
		return nil, errors.New("target has no address")
	}
	username := os.Getenv("TARGET_USER")
	password := os.Getenv("TARGET_PASS")
	if username == "" || password == "" {
		return nil, errors.New("gNMI credentials not set — export TARGET_USER and TARGET_PASS (the device account gnpctl connects with)")
	}
	return transport.NewLiveTransport(
		host, port, username, password, os.Getenv("GNMI_TLS_CA"),
	)
}

// ResolvePort is the effective port: explicit -> variant default by platform
// hint -> 57400.
func ResolvePort(target map[string]any, cat *catalog.LayeredCatalog) int {
	if port := intField(target, "port"); port != 0 {
		return port
	}
	hint := stringField(target, "platform_hint")
	if port := cat.DefaultPortForPlatform(&hint); port != nil {
		return *port
	}
	return transport.DefaultGNMIPort
}

// Discovery outcomes; the number of each is the discover command's summary.
const (
	StatusMatched = "matched"
	StatusUnknown = "unknown"
	StatusError   = "error"
)

// DiscoverOutcome is one target's discovery result.
type DiscoverOutcome struct {
	Status string
}

// DiscoverTarget discovers one target. Transport/probe errors are recorded on
// the target and returned in the outcome; only state-store errors are returned
// as err. report (optional) receives the progress lines.
func DiscoverTarget(
	target map[string]any,
	cat *catalog.LayeredCatalog,
	st *state.State,
	factory TransportFactory,
	now *int64,
	report io.Writer,
) (DiscoverOutcome, error) {
	name := stringField(target, "name")
	timestamp := time.Now().Unix()
	if now != nil {
		timestamp = *now
	}
	port := ResolvePort(target, cat)
	if err := st.SetTargetPort(name, port); err != nil {
		return DiscoverOutcome{Status: StatusError}, err
	}
	if err := st.EnsureModuleColumns(cat.Modules().Names()); err != nil {
		return DiscoverOutcome{Status: StatusError}, err
	}
	say(report, fmt.Sprintf("Fingerprinting %s", name))

	fail := func(phase string, cause error) (DiscoverOutcome, error) {
		message := describe(cause)
		slog.Warn(fmt.Sprintf("discover %s: %s error: %s", name, phase, message))
		if err := st.RecordTargetError(name, errors.New(message)); err != nil {
			return DiscoverOutcome{Status: StatusError}, err
		}
		say(report, fmt.Sprintf("%s: error: %s", name, message))
		return DiscoverOutcome{Status: StatusError}, nil
	}

	gnmi, err := factory(target, port)
	if err != nil {
		return fail("transport", err)
	}
	defer func() {
		if closer, ok := gnmi.(interface{ Close() error }); ok {
			closer.Close()
		}
	}()

	rawCaps, err := gnmi.Capabilities()
	if err != nil {
		return fail("transport", err)
	}

	profile := capabilities.Parse(rawCaps)
	vendorMatch := fingerprint.MatchVendor(profile, cat)

	if vendorMatch.Vendor == "" {
		if err := st.EnrichTarget(name, state.Enrichment{
			Discovered:       true,
			LastDiscoveryRun: timestamp,
			CapabilitiesHash: profile.Hash(),
			Port:             &port,
		}); err != nil {
			return DiscoverOutcome{Status: StatusUnknown}, err
		}
		if err := st.ReconcileModules(name, moduleDefaults(cat, nil)); err != nil {
			return DiscoverOutcome{Status: StatusUnknown}, err
		}
		say(report, fmt.Sprintf("%s: vendor not recognised", name))
		return DiscoverOutcome{Status: StatusUnknown}, nil
	}

	say(report, fmt.Sprintf("%s is %s, getting make/version", name, vendorMatch.Vendor))

	vendorDef := cat.Vendor(vendorMatch.Vendor)
	if vendorDef == nil {
		return fail("probe", fmt.Errorf("unknown vendor '%s'", vendorMatch.Vendor))
	}
	identity, err := detail.Resolve(gnmi, vendorDef)
	if err != nil {
		return fail("probe", err)
	}

	models := slices.Sorted(maps.Keys(profile.Models))
	selection := cat.ResolveVariant(vendorMatch.Vendor, models, identity.AsDict())
	variant := selection.Variant

	catalogKey := ""
	if variant != nil {
		catalogKey = variant.Key
	}
	osVersion := text(identity.OSVersion)
	if err := st.EnrichTarget(name, state.Enrichment{
		Discovered:       true,
		LastDiscoveryRun: timestamp,
		CatalogKey:       catalogKey,
		Vendor:           identity.Vendor,
		OS:               text(identity.OS),
		Model:            text(identity.Model),
		OSVersion:        osVersion,
		CapabilitiesHash: profile.Hash(),
		Port:             &port,
	}); err != nil {
		return DiscoverOutcome{Status: StatusError}, err
	}

	var effective *catalog.EffectiveProfile
	if variant != nil {
		var versionPtr *string
		if osVersion != "" {
			versionPtr = &osVersion
		}
		effective, err = cat.EffectiveProfile(variant.Key, versionPtr)
		if err != nil {
			return DiscoverOutcome{Status: StatusError}, err
		}
	}
	if err := st.ReconcileModules(name, moduleDefaults(cat, effective)); err != nil {
		return DiscoverOutcome{Status: StatusError}, err
	}

	descr := joinNonEmpty(identity.Vendor, text(identity.Model), osVersion)
	if variant == nil {
		say(report, fmt.Sprintf("%s is %s (no catalog variant)", name, descr))
		return DiscoverOutcome{Status: StatusUnknown}, nil
	}
	say(report, fmt.Sprintf("%s is %s", name, descr))
	return DiscoverOutcome{Status: StatusMatched}, nil
}

// describe is a non-empty error string — some transport/gRPC errors stringify
// to "".
func describe(cause error) string {
	message := strings.TrimSpace(cause.Error())
	if message == "" {
		return fmt.Sprintf("%T", cause)
	}
	return message
}

// moduleDefaults is discovery's default (1 on / 0 off) per registry module: on
// iff the effective profile defines the module with paths and its default is on.
func moduleDefaults(cat *catalog.LayeredCatalog, profile *catalog.EffectiveProfile) map[string]int {
	names := cat.Modules().Names()
	defaults := make(map[string]int, len(names))
	for _, name := range names {
		value := 0
		if profile != nil {
			if resolved, ok := profile.Modules.Get(name); ok && resolved != nil &&
				len(resolved.Paths) > 0 && resolved.Default {
				value = 1
			}
		}
		defaults[name] = value
	}
	return defaults
}

func say(report io.Writer, line string) {
	if report != nil {
		fmt.Fprintln(report, line)
	}
}

func stringField(row map[string]any, key string) string {
	s, _ := row[key].(string)
	return s
}

func intField(row map[string]any, key string) int {
	v, _ := row[key].(int64)
	return int(v)
}

// text renders an optional probe value the way the Python code stores it in a
// text column (nil -> unset).
func text(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func joinNonEmpty(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " ")
}
