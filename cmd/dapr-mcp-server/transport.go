package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dapr/dapr-mcp-server/pkg/eventstore"
)

const (
	statelessFlag   = "stateless"
	eventStoreFlag  = "event-store"
	specVersionFlag = "spec-version"

	// statelessOnlyVersion is the first protocol version without sessions.
	// The SDK serves it over streamable HTTP only in stateless mode.
	statelessOnlyVersion = "2026-07-28"

	// eventStoreCheckTimeout bounds the startup probe of the --event-store state store.
	eventStoreCheckTimeout = 10 * time.Second
	// eventStoreSessionTimeout closes sessions idle for as long as their events are kept,
	// so the event store's SessionClosed also runs for sessions a client abandons.
	eventStoreSessionTimeout = eventstore.DefaultTTL
)

// transportFlags holds the raw values of the transport flags.
type transportFlags struct {
	stateless   bool
	eventStore  string
	specVersion string
}

// registerTransportFlags defines the transport flags on fs.
func registerTransportFlags(fs *flag.FlagSet) *transportFlags {
	f := &transportFlags{}
	fs.BoolVar(&f.stateless, statelessFlag, false,
		"streamable HTTP only: serve without sessions (no Mcp-Session-Id); each request stands alone. "+
			"Required to serve protocol "+statelessOnlyVersion+" or later over HTTP; without it such clients fall back to an older version")
	fs.StringVar(&f.eventStore, eventStoreFlag, "",
		"streamable HTTP only: name of a Dapr state store that keeps SSE events, "+
			"so a client can resume a dropped stream with Last-Event-ID (sessions only; not with --"+statelessFlag+"). "+
			fmt.Sprintf("Also closes sessions that send no request for %.0f minutes, even while a GET stream is open. ", eventStoreSessionTimeout.Minutes())+
			"The events hold complete tool results, secrets and decrypted data included; "+
			"use a dedicated, app-scoped, encrypted state store")
	fs.StringVar(&f.specVersion, specVersionFlag, "",
		"newest MCP protocol version the server negotiates; older versions stay accepted. One of "+
			strings.Join(mcp.SupportedProtocolVersions(), ", ")+" (default the newest)")
	return f
}

// transportConfig is the validated form of transportFlags.
type transportConfig struct {
	stateless      bool
	eventStoreName string
	// specVersion is the newest protocol version the server is configured to negotiate.
	specVersion string
	// effectiveVersion is the newest version this transport actually negotiates. Streamable
	// HTTP with sessions stops before statelessOnlyVersion, whatever specVersion says.
	effectiveVersion string
	// protocolVersions narrows the SDK's supported versions; nil keeps them all.
	protocolVersions []string
	// warnings describe flags that have no effect in this configuration.
	warnings []string
}

// resolve validates the flags. httpMode reports whether the server uses streamable HTTP,
// since --stateless and --event-store only apply there.
func (f *transportFlags) resolve(httpMode bool) (transportConfig, error) {
	cfg := transportConfig{stateless: f.stateless, eventStoreName: f.eventStore}

	if f.eventStore != "" && strings.TrimSpace(f.eventStore) == "" {
		return transportConfig{}, fmt.Errorf("--%s must name a Dapr state store", eventStoreFlag)
	}
	if f.stateless && f.eventStore != "" {
		return transportConfig{}, fmt.Errorf("--%s needs sessions to resume, so it cannot be combined with --%s", eventStoreFlag, statelessFlag)
	}

	versions, err := protocolVersionsUpTo(f.specVersion)
	if err != nil {
		return transportConfig{}, err
	}
	cfg.protocolVersions = versions
	cfg.specVersion = mcp.SupportedProtocolVersions()[0]
	if versions != nil {
		cfg.specVersion = versions[0]
	}

	cfg.effectiveVersion = effectiveVersion(cfg, httpMode)
	cfg.warnings = flagWarnings(cfg, httpMode, f.specVersion != "")
	return cfg, nil
}

// effectiveVersion returns the newest version the transport negotiates: specVersion, capped
// below statelessOnlyVersion for streamable HTTP with sessions, where the SDK does not serve it.
func effectiveVersion(cfg transportConfig, httpMode bool) string {
	if !httpMode || cfg.stateless || cfg.specVersion < statelessOnlyVersion {
		return cfg.specVersion
	}
	for _, v := range mcp.SupportedProtocolVersions() {
		if v < statelessOnlyVersion {
			return v
		}
	}
	return cfg.specVersion
}

// protocolVersionsUpTo returns the SDK-supported versions no newer than newest, newest first.
// An empty newest returns nil, which leaves the SDK default in place.
func protocolVersionsUpTo(newest string) ([]string, error) {
	if newest == "" {
		return nil, nil
	}
	supported := mcp.SupportedProtocolVersions()
	if !slices.Contains(supported, newest) {
		return nil, fmt.Errorf("--%s %q is not one of the supported versions: %s",
			specVersionFlag, newest, strings.Join(supported, ", "))
	}
	// Versions are ISO dates, so string order is date order.
	return slices.DeleteFunc(supported, func(v string) bool { return v > newest }), nil
}

// flagWarnings lists flags that have no effect. pinned reports whether --spec-version was given.
func flagWarnings(cfg transportConfig, httpMode, pinned bool) []string {
	var warnings []string
	if !httpMode {
		if cfg.stateless {
			warnings = append(warnings, fmt.Sprintf("--%s has no effect over stdio; it applies to --http only", statelessFlag))
		}
		if cfg.eventStoreName != "" {
			warnings = append(warnings, fmt.Sprintf("--%s has no effect over stdio; it applies to --http only", eventStoreFlag))
		}
		return warnings
	}
	if pinned && !cfg.stateless && cfg.specVersion >= statelessOnlyVersion {
		warnings = append(warnings, fmt.Sprintf(
			"protocol %s and later is served over HTTP only with --%s; session clients will negotiate an older version",
			statelessOnlyVersion, statelessFlag))
	}
	return warnings
}

// streamableOptions builds the streamable HTTP handler options. The SDK logs to logger.
// state is the Dapr client the event store writes through; it may be nil when no event store
// is configured. A configured event store is probed with a write and a delete, so a state
// store that is missing, unreachable, read-only or without TTL support stops the server at
// startup instead of failing every session.
func (c transportConfig) streamableOptions(ctx context.Context, state eventstore.StateClient, logger *slog.Logger) (*mcp.StreamableHTTPOptions, error) {
	opts := &mcp.StreamableHTTPOptions{Stateless: c.stateless, Logger: logger}
	if c.eventStoreName == "" {
		return opts, nil
	}
	if state == nil {
		return nil, errors.New("event store: no Dapr client to write through")
	}
	store, err := eventstore.New(state, eventstore.Options{StoreName: c.eventStoreName})
	if err != nil {
		return nil, fmt.Errorf("create event store: %w", err)
	}
	checkCtx, cancel := context.WithTimeout(ctx, eventStoreCheckTimeout)
	defer cancel()
	if err := store.Check(checkCtx); err != nil {
		return nil, fmt.Errorf("--%s %q: %w", eventStoreFlag, c.eventStoreName, err)
	}
	logger.Info("Stream resumption enabled", "state_store", c.eventStoreName)
	logger.Warn("Resumable events hold complete tool results, including secret values and decrypted data; "+
		"use a state store dedicated to this server, scoped to it and encrypted", "state_store", c.eventStoreName)
	opts.EventStore = eventstore.WithLogging(store, logger)
	opts.SessionTimeout = eventStoreSessionTimeout
	return opts, nil
}
