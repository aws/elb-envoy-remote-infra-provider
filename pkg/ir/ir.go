// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package ir provides a light domain wrapper around the generated protobuf
// Infra message, plus the IR helper types and constants that consumers rely on.
//
// Code threads *ir.Infra rather than the raw *pb.Infra so that IR-level helper
// behaviour lives on the wrapper instead of on the generated wire type. The
// wrapper embeds *pb.Infra, so all generated fields and getters remain directly
// accessible, and it memoizes the deserialized EnvoyProxy config so the JSON
// Config bytes are only unmarshalled once.
package ir

import (
	"encoding/json"
	"fmt"
	"sync"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
)

const (
	// DefaultProxyName is the name used for managed proxy infrastructure when
	// none is provided.
	DefaultProxyName = "default"
)

// Listener protocol values (mirror the old ir.ProtocolType constants). The
// generated ListenerPort.Protocol field is a plain string, so these are plain
// string constants that assign to and compare with it directly.
const (
	// HTTPProtocolType accepts cleartext HTTP/1.1 or HTTP/2 sessions.
	HTTPProtocolType = "HTTP"
	// HTTPSProtocolType accepts HTTP/1.1 or HTTP/2 sessions over TLS.
	HTTPSProtocolType = "HTTPS"
	// TLSProtocolType accepts TLS sessions over TCP.
	TLSProtocolType = "TLS"
	// TCPProtocolType accepts TCP connections.
	TCPProtocolType = "TCP"
	// UDPProtocolType accepts UDP connections.
	UDPProtocolType = "UDP"
)

// PortProtocol is a comparable port/protocol pair. It replaces ir.PortProtocol
// and is safe to use as a map key and with slices.Equal, unlike the generated
// ListenerPort message.
type PortProtocol struct {
	Protocol string
	Port     int32
}

// Infra wraps the generated pb.Infra with IR helper behaviour and a memoized
// EnvoyProxy config.
//
// A wrapper instance may be shared (it is held in the reconciliation cache), so
// access to the memoized config is guarded by a mutex to keep GetProxyConfig
// safe for concurrent use.
type Infra struct {
	*pb.Infra

	// configMu guards the memoized config fields below.
	configMu sync.RWMutex
	// proxyConfig caches the deserialized EnvoyProxy config. The wire Config is
	// carried as opaque JSON bytes; caching the parsed value means the bytes are
	// only unmarshalled once per wrapper.
	proxyConfig       *egv1a1.EnvoyProxy
	proxyConfigParsed bool
}

// New wraps a pb.Infra. It returns nil when the wire infra is nil so callers can
// keep using nil checks on the wrapper.
func New(infra *pb.Infra) *Infra {
	if infra == nil {
		return nil
	}
	return &Infra{Infra: infra}
}

// NewProxyInfra returns a new ProxyInfra with default parameters.
func NewProxyInfra() *pb.ProxyInfra {
	return &pb.ProxyInfra{
		Metadata: &pb.InfraMetadata{Labels: map[string]string{}},
		Name:     DefaultProxyName,
	}
}

// GetProxyInfra returns the ProxyInfra, initialising it and its defaulted
// sub-fields if necessary. It mirrors the behaviour of the deleted
// ir.Infra.GetProxyInfra.
func (i *Infra) GetProxyInfra() *pb.ProxyInfra {
	if i.Proxy == nil {
		i.Proxy = NewProxyInfra()
		return i.Proxy
	}
	if len(i.Proxy.Name) == 0 {
		i.Proxy.Name = DefaultProxyName
	}
	if len(i.Proxy.Listeners) == 0 {
		i.Proxy.Listeners = []*pb.ProxyListener{{}}
	}
	if i.Proxy.Metadata == nil {
		i.Proxy.Metadata = &pb.InfraMetadata{Labels: map[string]string{}}
	}
	return i.Proxy
}

// GetProxyConfig returns the EnvoyProxy config, deserializing the JSON Config
// bytes on first use and memoizing the result for subsequent calls. Malformed or
// empty Config yields an empty EnvoyProxy so callers can read spec fields
// without nil checks.
func (i *Infra) GetProxyConfig() *egv1a1.EnvoyProxy {
	// Fast path: the config has already been parsed. Concurrent readers share the
	// read lock.
	i.configMu.RLock()
	if i.proxyConfigParsed {
		cfg := i.proxyConfig
		i.configMu.RUnlock()
		return cfg
	}
	i.configMu.RUnlock()

	// Slow path: parse under the write lock, re-checking in case another
	// goroutine parsed while we waited for the lock.
	i.configMu.Lock()
	defer i.configMu.Unlock()
	if i.proxyConfigParsed {
		return i.proxyConfig
	}

	cfg := new(egv1a1.EnvoyProxy)
	if raw := i.GetProxyInfra().Config; len(raw) > 0 {
		if err := json.Unmarshal(raw, cfg); err != nil {
			// Degrade gracefully: an unparseable config is treated as absent, the
			// same as the zero value the old typed field would have held.
			cfg = new(egv1a1.EnvoyProxy)
		}
	}

	i.proxyConfig = cfg
	i.proxyConfigParsed = true
	return i.proxyConfig
}

// SetProxyConfig JSON-encodes cfg into the ProxyInfra Config bytes.
func SetProxyConfig(p *pb.ProxyInfra, cfg *egv1a1.EnvoyProxy) error {
	if cfg == nil {
		p.Config = nil
		return nil
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal EnvoyProxy config: %w", err)
	}
	p.Config = b
	return nil
}
