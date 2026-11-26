// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package ir

import (
	"sync"
	"testing"

	egv1a1 "github.com/envoyproxy/gateway/api/v1alpha1"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNew(t *testing.T) {
	t.Run("nil wire infra yields nil wrapper", func(t *testing.T) {
		assert.Nil(t, New(nil))
	})

	t.Run("non-nil wire infra is wrapped and embedded fields are accessible", func(t *testing.T) {
		wire := &pb.Infra{Proxy: &pb.ProxyInfra{Name: "ns/gw"}}
		w := New(wire)
		require.NotNil(t, w)
		assert.Same(t, wire, w.Infra)
		assert.Equal(t, "ns/gw", w.Proxy.Name)
	})
}

func TestNewProxyInfra(t *testing.T) {
	p := NewProxyInfra()
	assert.Equal(t, DefaultProxyName, p.Name)
	require.NotNil(t, p.Metadata)
	assert.NotNil(t, p.Metadata.Labels)
}

func TestGetProxyInfra(t *testing.T) {
	t.Run("nil proxy is initialised with defaults", func(t *testing.T) {
		w := New(&pb.Infra{})
		got := w.GetProxyInfra()
		require.NotNil(t, got)
		assert.Equal(t, DefaultProxyName, got.Name)
		require.NotNil(t, got.Metadata)
		// The wrapper's Proxy field is set in place.
		assert.Same(t, got, w.Proxy)
	})

	t.Run("empty proxy sub-fields are defaulted", func(t *testing.T) {
		w := New(&pb.Infra{Proxy: &pb.ProxyInfra{}})
		got := w.GetProxyInfra()
		assert.Equal(t, DefaultProxyName, got.Name)
		assert.Len(t, got.Listeners, 1, "an empty listener should be seeded")
		require.NotNil(t, got.Metadata)
		assert.NotNil(t, got.Metadata.Labels)
	})

	t.Run("populated proxy is preserved", func(t *testing.T) {
		proxy := &pb.ProxyInfra{
			Name:      "ns/gw",
			Listeners: []*pb.ProxyListener{{Name: "http"}},
			Metadata:  &pb.InfraMetadata{Labels: map[string]string{"k": "v"}},
		}
		w := New(&pb.Infra{Proxy: proxy})
		got := w.GetProxyInfra()
		assert.Equal(t, "ns/gw", got.Name)
		assert.Len(t, got.Listeners, 1)
		assert.Equal(t, "v", got.Metadata.Labels["k"])
	})
}

func TestGetProxyConfig(t *testing.T) {
	ipv6 := egv1a1.IPv6

	t.Run("empty config yields a non-nil empty EnvoyProxy", func(t *testing.T) {
		w := New(&pb.Infra{Proxy: &pb.ProxyInfra{}})
		cfg := w.GetProxyConfig()
		require.NotNil(t, cfg)
		assert.Nil(t, cfg.Spec.IPFamily)
	})

	t.Run("valid config bytes are deserialized", func(t *testing.T) {
		proxy := &pb.ProxyInfra{}
		require.NoError(t, SetProxyConfig(proxy, &egv1a1.EnvoyProxy{
			Spec: egv1a1.EnvoyProxySpec{IPFamily: &ipv6},
		}))
		w := New(&pb.Infra{Proxy: proxy})

		cfg := w.GetProxyConfig()
		require.NotNil(t, cfg.Spec.IPFamily)
		assert.Equal(t, egv1a1.IPv6, *cfg.Spec.IPFamily)
	})

	t.Run("malformed config bytes degrade to an empty EnvoyProxy", func(t *testing.T) {
		w := New(&pb.Infra{Proxy: &pb.ProxyInfra{Config: []byte("{not-json")}})
		cfg := w.GetProxyConfig()
		require.NotNil(t, cfg)
		assert.Nil(t, cfg.Spec.IPFamily)
	})

	t.Run("config is deserialized only once and memoized", func(t *testing.T) {
		proxy := &pb.ProxyInfra{}
		require.NoError(t, SetProxyConfig(proxy, &egv1a1.EnvoyProxy{
			Spec: egv1a1.EnvoyProxySpec{IPFamily: &ipv6},
		}))
		w := New(&pb.Infra{Proxy: proxy})

		first := w.GetProxyConfig()

		// Mutate the underlying bytes after the first parse. A memoized wrapper must
		// return the previously parsed value and ignore the changed bytes.
		w.Proxy.Config = []byte(`{"spec":{}}`)
		second := w.GetProxyConfig()

		assert.Same(t, first, second, "GetProxyConfig should return the memoized value")
		require.NotNil(t, second.Spec.IPFamily, "memoized value must reflect the original parse")
	})
}

func TestGetProxyConfig_Concurrent(t *testing.T) {
	// Run with -race to verify the mutex makes concurrent memoization safe.
	ipv6 := egv1a1.IPv6
	proxy := &pb.ProxyInfra{}
	require.NoError(t, SetProxyConfig(proxy, &egv1a1.EnvoyProxy{
		Spec: egv1a1.EnvoyProxySpec{IPFamily: &ipv6},
	}))
	w := New(&pb.Infra{Proxy: proxy})

	const goroutines = 50
	var wg sync.WaitGroup
	results := make([]*egv1a1.EnvoyProxy, goroutines)
	wg.Add(goroutines)
	for idx := 0; idx < goroutines; idx++ {
		go func(idx int) {
			defer wg.Done()
			results[idx] = w.GetProxyConfig()
		}(idx)
	}
	wg.Wait()

	// Every goroutine must observe the same memoized pointer.
	for idx := 1; idx < goroutines; idx++ {
		assert.Same(t, results[0], results[idx])
	}
	require.NotNil(t, results[0].Spec.IPFamily)
}

func TestSetProxyConfig(t *testing.T) {
	t.Run("nil config clears the bytes", func(t *testing.T) {
		proxy := &pb.ProxyInfra{Config: []byte("stale")}
		require.NoError(t, SetProxyConfig(proxy, nil))
		assert.Nil(t, proxy.Config)
	})

	t.Run("round trips through GetProxyConfig", func(t *testing.T) {
		ipv6 := egv1a1.IPv6
		proxy := &pb.ProxyInfra{}
		require.NoError(t, SetProxyConfig(proxy, &egv1a1.EnvoyProxy{
			Spec: egv1a1.EnvoyProxySpec{IPFamily: &ipv6},
		}))
		assert.NotEmpty(t, proxy.Config)

		cfg := New(&pb.Infra{Proxy: proxy}).GetProxyConfig()
		require.NotNil(t, cfg.Spec.IPFamily)
		assert.Equal(t, egv1a1.IPv6, *cfg.Spec.IPFamily)
	})
}

func TestPortProtocolIsComparable(t *testing.T) {
	// PortProtocol must be usable as a map key (the reason it exists rather than
	// reusing the non-comparable generated ListenerPort).
	seen := map[PortProtocol]struct{}{
		{Protocol: "tcp", Port: 80}: {},
		{Protocol: "udp", Port: 53}: {},
	}
	_, ok := seen[PortProtocol{Protocol: "tcp", Port: 80}]
	assert.True(t, ok)
	_, ok = seen[PortProtocol{Protocol: "tcp", Port: 443}]
	assert.False(t, ok)
}
