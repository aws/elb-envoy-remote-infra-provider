// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package infrastore

import (
	"testing"

	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

func TestCache_Infra(t *testing.T) {
	infra := ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Name: "default/gw"}})
	tests := []struct {
		name      string
		setup     func(*Cache)
		key       string
		wantFound bool
		wantValue *ir.Infra
	}{
		{
			name:      "not found on empty cache",
			setup:     func(c *Cache) {},
			key:       "default/gw",
			wantFound: false,
		},
		{
			name:      "found after set",
			setup:     func(c *Cache) { c.SetInfra("default/gw", infra) },
			key:       "default/gw",
			wantFound: true,
			wantValue: infra,
		},
		{
			name: "not found after delete",
			setup: func(c *Cache) {
				c.SetInfra("default/gw", infra)
				c.DeleteInfra("default/gw")
			},
			key:       "default/gw",
			wantFound: false,
		},
		{
			name:      "different key not found",
			setup:     func(c *Cache) { c.SetInfra("ns/gw1", infra) },
			key:       "ns/gw2",
			wantFound: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.setup(c)
			got, ok := c.GetInfra(tt.key)
			assert.Equal(t, tt.wantFound, ok)
			if tt.wantFound {
				assert.Equal(t, tt.wantValue, got)
			}
		})
	}
}

func TestCache_AllInfraKeys(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(*Cache)
		wantKeys []string
	}{
		{
			name:     "empty cache",
			setup:    func(c *Cache) {},
			wantKeys: []string{},
		},
		{
			name:     "single key",
			setup:    func(c *Cache) { c.SetInfra("ns/gw1", ir.New(&pb.Infra{})) },
			wantKeys: []string{"ns/gw1"},
		},
		{
			name: "multiple keys",
			setup: func(c *Cache) {
				c.SetInfra("ns/gw1", ir.New(&pb.Infra{}))
				c.SetInfra("ns/gw2", ir.New(&pb.Infra{}))
			},
			wantKeys: []string{"ns/gw1", "ns/gw2"},
		},
		{
			name: "key removed after delete",
			setup: func(c *Cache) {
				c.SetInfra("ns/gw1", ir.New(&pb.Infra{}))
				c.SetInfra("ns/gw2", ir.New(&pb.Infra{}))
				c.DeleteInfra("ns/gw1")
			},
			wantKeys: []string{"ns/gw2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.setup(c)
			assert.ElementsMatch(t, tt.wantKeys, c.AllInfraKeys())
		})
	}
}

func TestCache_XDSHosts(t *testing.T) {
	tests := []struct {
		name        string
		operations  [][]string
		wantChanged []bool
		wantFinal   []string
	}{
		{
			name:        "initial set — changed",
			operations:  [][]string{{"10.0.0.1"}},
			wantChanged: []bool{true},
			wantFinal:   []string{"10.0.0.1"},
		},
		{
			name:        "same value — no change",
			operations:  [][]string{{"10.0.0.1"}, {"10.0.0.1"}},
			wantChanged: []bool{true, false},
			wantFinal:   []string{"10.0.0.1"},
		},
		{
			name:        "different value — changed",
			operations:  [][]string{{"10.0.0.1"}, {"10.0.0.2"}},
			wantChanged: []bool{true, true},
			wantFinal:   []string{"10.0.0.2"},
		},
		{
			name:        "multiple hosts — changed",
			operations:  [][]string{{"10.0.0.1", "10.0.0.2"}},
			wantChanged: []bool{true},
			wantFinal:   []string{"10.0.0.1", "10.0.0.2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			assert.Empty(t, c.XDSHosts())
			for i, hosts := range tt.operations {
				assert.Equal(t, tt.wantChanged[i], c.SetXDSHosts(hosts), "op %d", i)
			}
			assert.Equal(t, tt.wantFinal, c.XDSHosts())
		})
	}
}

func TestCache_BackendPorts(t *testing.T) {
	ports := []ir.PortProtocol{{Protocol: "tcp", Port: 8080}}
	ports2 := []ir.PortProtocol{{Protocol: "tcp", Port: 9090}}
	tests := []struct {
		name        string
		setup       func(*Cache)
		setPorts    []ir.PortProtocol
		wantChanged bool
		wantPorts   []ir.PortProtocol
	}{
		{
			name:        "nil on empty — no change",
			setup:       func(c *Cache) {},
			setPorts:    nil,
			wantChanged: false,
		},
		{
			name:        "set ports — changed",
			setup:       func(c *Cache) {},
			setPorts:    ports,
			wantChanged: true,
			wantPorts:   ports,
		},
		{
			name:        "same ports — no change",
			setup:       func(c *Cache) { c.SetBackendPorts("ns/gw", ports) },
			setPorts:    ports,
			wantChanged: false,
			wantPorts:   ports,
		},
		{
			name:        "different ports — changed",
			setup:       func(c *Cache) { c.SetBackendPorts("ns/gw", ports) },
			setPorts:    ports2,
			wantChanged: true,
			wantPorts:   ports2,
		},
		{
			name:        "nil removes existing — changed",
			setup:       func(c *Cache) { c.SetBackendPorts("ns/gw", ports) },
			setPorts:    nil,
			wantChanged: true,
			wantPorts:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.setup(c)
			assert.Equal(t, tt.wantChanged, c.SetBackendPorts("ns/gw", tt.setPorts))
			assert.Equal(t, tt.wantPorts, c.GetBackendPorts("ns/gw"))
		})
	}
}

func TestPortsEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []ir.PortProtocol
		want bool
	}{
		{"both nil", nil, nil, true},
		{"equal", []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, true},
		{"different port", []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, []ir.PortProtocol{{Protocol: "tcp", Port: 443}}, false},
		{"different protocol", []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, []ir.PortProtocol{{Protocol: "udp", Port: 80}}, false},
		{"different lengths", []ir.PortProtocol{{Protocol: "tcp", Port: 80}}, nil, false},
		{
			"same entries different order",
			[]ir.PortProtocol{{Protocol: "tcp", Port: 443}, {Protocol: "tcp", Port: 80}},
			[]ir.PortProtocol{{Protocol: "tcp", Port: 80}, {Protocol: "tcp", Port: 443}},
			true,
		},
		{
			"same ports different protocols different order",
			[]ir.PortProtocol{{Protocol: "udp", Port: 53}, {Protocol: "tcp", Port: 80}},
			[]ir.PortProtocol{{Protocol: "tcp", Port: 80}, {Protocol: "udp", Port: 53}},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, portsEqual(tt.a, tt.b))
		})
	}
}

func TestCache_GatewayClassIndex(t *testing.T) {
	tests := []struct {
		name         string
		ops          func(*Cache)
		queryKey     string
		wantClass    string
		wantFound    bool
		queryClass   string
		wantGateways []string
	}{
		{
			name:       "unknown gateway — not found",
			ops:        func(c *Cache) {},
			queryKey:   "ns/gw",
			wantFound:  false,
			queryClass: "eg",
		},
		{
			name:         "single gateway — forward and reverse lookup",
			ops:          func(c *Cache) { c.SetGatewayClass("ns/gw", "eg") },
			queryKey:     "ns/gw",
			wantClass:    "eg",
			wantFound:    true,
			queryClass:   "eg",
			wantGateways: []string{"ns/gw"},
		},
		{
			name: "two gateways same class — reverse returns both",
			ops: func(c *Cache) {
				c.SetGatewayClass("ns/gw1", "eg")
				c.SetGatewayClass("ns/gw2", "eg")
			},
			queryKey:     "ns/gw1",
			wantClass:    "eg",
			wantFound:    true,
			queryClass:   "eg",
			wantGateways: []string{"ns/gw1", "ns/gw2"},
		},
		{
			name: "gateway changes class — reverse index re-keyed",
			ops: func(c *Cache) {
				c.SetGatewayClass("ns/gw", "old")
				c.SetGatewayClass("ns/gw", "new")
			},
			queryKey:     "ns/gw",
			wantClass:    "new",
			wantFound:    true,
			queryClass:   "old",
			wantGateways: nil,
		},
		{
			name:       "empty className cached — forward miss, reverse empty",
			ops:        func(c *Cache) { c.SetGatewayClass("ns/gw", "") },
			queryKey:   "ns/gw",
			wantFound:  false,
			queryClass: "",
		},
		{
			name: "DeleteInfra clears index entries",
			ops: func(c *Cache) {
				c.SetInfra("ns/gw", ir.New(&pb.Infra{}))
				c.SetGatewayClass("ns/gw", "eg")
				c.DeleteInfra("ns/gw")
			},
			queryKey:   "ns/gw",
			wantFound:  false,
			queryClass: "eg",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.ops(c)
			got, ok := c.GetGatewayClass(tt.queryKey)
			assert.Equal(t, tt.wantFound, ok)
			if tt.wantFound {
				assert.Equal(t, tt.wantClass, got)
			}
			assert.ElementsMatch(t, tt.wantGateways, c.GatewaysInClass(tt.queryClass))
		})
	}
}

func TestCache_EffectiveProxyIndex(t *testing.T) {
	tests := []struct {
		name         string
		ops          func(*Cache)
		queryKey     string
		wantProxy    string
		wantFound    bool
		queryProxy   string
		wantGateways []string
	}{
		{
			name:       "unknown gateway — not found",
			ops:        func(c *Cache) {},
			queryKey:   "ns/gw",
			wantFound:  false,
			queryProxy: "ns/p",
		},
		{
			name:         "single gateway attached — forward and reverse lookup",
			ops:          func(c *Cache) { c.SetEffectiveProxy("ns/gw", "ns/p") },
			queryKey:     "ns/gw",
			wantProxy:    "ns/p",
			wantFound:    true,
			queryProxy:   "ns/p",
			wantGateways: []string{"ns/gw"},
		},
		{
			name: "gateway switches proxy — reverse index re-keyed",
			ops: func(c *Cache) {
				c.SetEffectiveProxy("ns/gw", "ns/old")
				c.SetEffectiveProxy("ns/gw", "ns/new")
			},
			queryKey:     "ns/gw",
			wantProxy:    "ns/new",
			wantFound:    true,
			queryProxy:   "ns/old",
			wantGateways: nil,
		},
		{
			name: "multiple gateways share proxy — reverse returns all",
			ops: func(c *Cache) {
				c.SetEffectiveProxy("ns/gw1", "ns/p")
				c.SetEffectiveProxy("ns/gw2", "ns/p")
			},
			queryProxy:   "ns/p",
			queryKey:     "ns/gw1",
			wantProxy:    "ns/p",
			wantFound:    true,
			wantGateways: []string{"ns/gw1", "ns/gw2"},
		},
		{
			name:       "empty proxyKey cached — forward miss, reverse empty",
			ops:        func(c *Cache) { c.SetEffectiveProxy("ns/gw", "") },
			queryKey:   "ns/gw",
			wantFound:  false,
			queryProxy: "",
		},
		{
			name: "DeleteInfra clears index entries",
			ops: func(c *Cache) {
				c.SetInfra("ns/gw", ir.New(&pb.Infra{}))
				c.SetEffectiveProxy("ns/gw", "ns/p")
				c.DeleteInfra("ns/gw")
			},
			queryKey:   "ns/gw",
			wantFound:  false,
			queryProxy: "ns/p",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.ops(c)
			got, ok := c.GetEffectiveProxy(tt.queryKey)
			assert.Equal(t, tt.wantFound, ok)
			if tt.wantFound {
				assert.Equal(t, tt.wantProxy, got)
			}
			assert.ElementsMatch(t, tt.wantGateways, c.GatewaysForEffectiveProxy(tt.queryProxy))
		})
	}
}

func TestCache_ELBProxyConfiguration(t *testing.T) {
	makeProxy := func(ns, name string) *elbproxyv1alpha1.ELBProxyConfiguration {
		return &elbproxyv1alpha1.ELBProxyConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		}
	}

	tests := []struct {
		name      string
		setup     func(*Cache)
		key       string
		wantFound bool
	}{
		{
			name:      "not found on empty cache",
			setup:     func(c *Cache) {},
			key:       "default/p",
			wantFound: false,
		},
		{
			name:      "found after set",
			setup:     func(c *Cache) { c.SetELBProxyConfiguration("default/p", makeProxy("default", "p")) },
			key:       "default/p",
			wantFound: true,
		},
		{
			name: "not found after delete",
			setup: func(c *Cache) {
				c.SetELBProxyConfiguration("default/p", makeProxy("default", "p"))
				c.DeleteELBProxyConfiguration("default/p")
			},
			key:       "default/p",
			wantFound: false,
		},
		{
			name: "two proxies same name different namespace — no collision",
			setup: func(c *Cache) {
				c.SetELBProxyConfiguration("ns-a/p", makeProxy("ns-a", "p"))
				c.SetELBProxyConfiguration("ns-b/p", makeProxy("ns-b", "p"))
			},
			key:       "ns-a/p",
			wantFound: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.setup(c)
			got, ok := c.GetELBProxyConfiguration(tt.key)
			assert.Equal(t, tt.wantFound, ok)
			if tt.wantFound {
				assert.NotNil(t, got)
			}
		})
	}
}

func TestCache_AllELBProxyConfigurations(t *testing.T) {
	makeProxy := func(ns, name string) *elbproxyv1alpha1.ELBProxyConfiguration {
		return &elbproxyv1alpha1.ELBProxyConfiguration{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		}
	}
	tests := []struct {
		name      string
		setup     func(*Cache)
		wantCount int
	}{
		{name: "empty cache", setup: func(c *Cache) {}, wantCount: 0},
		{
			name:      "single proxy",
			setup:     func(c *Cache) { c.SetELBProxyConfiguration("ns/p", makeProxy("ns", "p")) },
			wantCount: 1,
		},
		{
			name: "two proxies",
			setup: func(c *Cache) {
				c.SetELBProxyConfiguration("ns/p1", makeProxy("ns", "p1"))
				c.SetELBProxyConfiguration("ns/p2", makeProxy("ns", "p2"))
			},
			wantCount: 2,
		},
		{
			name: "deleted proxy not returned",
			setup: func(c *Cache) {
				c.SetELBProxyConfiguration("ns/p1", makeProxy("ns", "p1"))
				c.SetELBProxyConfiguration("ns/p2", makeProxy("ns", "p2"))
				c.DeleteELBProxyConfiguration("ns/p1")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New()
			tt.setup(c)
			assert.Len(t, c.AllELBProxyConfigurations(), tt.wantCount)
		})
	}
}

func TestCache_KnownKeys(t *testing.T) {
	infra := ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Name: "default/gw"}})

	t.Run("SetInfra marks the key known", func(t *testing.T) {
		c := New()
		assert.False(t, c.IsKnownKey("default/gw"))
		c.SetInfra("default/gw", infra)
		assert.True(t, c.IsKnownKey("default/gw"))
		assert.ElementsMatch(t, []string{"default/gw"}, c.AllKnownKeys())
	})

	t.Run("AddKnownKey marks a key known without IR", func(t *testing.T) {
		c := New()
		c.AddKnownKey("ns/seeded")
		assert.True(t, c.IsKnownKey("ns/seeded"))
		// No IR was set, so GetInfra must still miss.
		_, ok := c.GetInfra("ns/seeded")
		assert.False(t, ok)
	})

	t.Run("DeleteInfra evicts IR but the key stays known", func(t *testing.T) {
		c := New()
		c.SetInfra("default/gw", infra)
		c.DeleteInfra("default/gw")
		_, ok := c.GetInfra("default/gw")
		assert.False(t, ok, "IR should be evicted")
		assert.True(t, c.IsKnownKey("default/gw"), "key must remain known after IR eviction")
	})

	t.Run("ForgetKey removes both IR and known marker", func(t *testing.T) {
		c := New()
		c.SetInfra("default/gw", infra)
		c.ForgetKey("default/gw")
		_, ok := c.GetInfra("default/gw")
		assert.False(t, ok)
		assert.False(t, c.IsKnownKey("default/gw"))
		assert.Empty(t, c.AllKnownKeys())
	})
}

func TestCache_PendingDeletion(t *testing.T) {
	infra := ir.New(&pb.Infra{Proxy: &pb.ProxyInfra{Name: "default/gw"}})

	t.Run("MarkPendingDeletion sets pending and clears IR and known", func(t *testing.T) {
		c := New()
		c.SetInfra("default/gw", infra)
		assert.True(t, c.IsKnownKey("default/gw"))

		c.MarkPendingDeletion("default/gw")

		assert.True(t, c.IsPendingDeletion("default/gw"))
		assert.False(t, c.IsKnownKey("default/gw"))
		_, ok := c.GetInfra("default/gw")
		assert.False(t, ok)
	})

	t.Run("IsPendingDeletion returns false for unknown keys", func(t *testing.T) {
		c := New()
		assert.False(t, c.IsPendingDeletion("nonexistent"))
	})

	t.Run("ForgetKey clears pendingDeletion", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("default/gw")
		assert.True(t, c.IsPendingDeletion("default/gw"))

		c.ForgetKey("default/gw")
		assert.False(t, c.IsPendingDeletion("default/gw"))
	})

	t.Run("SetInfra cancels pending deletion", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("default/gw")
		c.SetInfra("default/gw", infra)
		assert.False(t, c.IsPendingDeletion("default/gw"))
	})

	t.Run("ForgetKeyIfPendingDeletion forgets only pending keys", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("default/gw")
		assert.True(t, c.ForgetKeyIfPendingDeletion("default/gw"))
		assert.False(t, c.IsPendingDeletion("default/gw"))

		c.MarkPendingDeletion("default/gw")
		c.SetInfra("default/gw", infra)
		assert.False(t, c.ForgetKeyIfPendingDeletion("default/gw"))
		_, ok := c.GetInfra("default/gw")
		assert.True(t, ok)
		assert.True(t, c.IsKnownKey("default/gw"))
	})

	t.Run("MarkPendingDeletion clears backend ports", func(t *testing.T) {
		c := New()
		c.SetInfra("default/gw", infra)
		c.SetBackendPorts("default/gw", []ir.PortProtocol{{Port: 8080, Protocol: "TCP"}})

		c.MarkPendingDeletion("default/gw")

		assert.Nil(t, c.GetBackendPorts("default/gw"))
	})

	t.Run("pending key not in AllInfraKeys or AllKnownKeys", func(t *testing.T) {
		c := New()
		c.SetInfra("default/gw", infra)
		c.MarkPendingDeletion("default/gw")

		assert.Empty(t, c.AllInfraKeys())
		assert.Empty(t, c.AllKnownKeys())
	})

	t.Run("HasPendingDeletionsExcluding returns false when empty", func(t *testing.T) {
		c := New()
		assert.False(t, c.HasPendingDeletionsExcluding(nil))
	})

	t.Run("HasPendingDeletionsExcluding returns true when other keys pending", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("ns/gw-1")
		c.MarkPendingDeletion("ns/gw-2")

		assert.True(t, c.HasPendingDeletionsExcluding([]string{"ns/gw-1"}))
	})

	t.Run("HasPendingDeletionsExcluding returns false when only excluded key pending", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("ns/gw-1")

		assert.False(t, c.HasPendingDeletionsExcluding([]string{"ns/gw-1"}))
	})

	t.Run("HasPendingDeletionsExcluding returns false when all keys excluded", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("ns/gw-1")
		c.MarkPendingDeletion("ns/gw-2")

		assert.False(t, c.HasPendingDeletionsExcluding([]string{"ns/gw-1", "ns/gw-2"}))
	})

	t.Run("HasPendingDeletionsExcluding with nil exclusions checks all keys", func(t *testing.T) {
		c := New()
		c.MarkPendingDeletion("ns/gw-1")

		assert.True(t, c.HasPendingDeletionsExcluding(nil))
	})
}
