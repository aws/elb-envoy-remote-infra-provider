// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package infrastore

import (
	"cmp"
	"slices"
	"sync"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
)

// Cache holds the in-memory reconciliation state.
// All triggers (upstream InfraIR, ELBProxyConfiguration CRD change, xDS endpoint change, route change)
// read and write through this cache before calling the reconciler.
type Cache struct {
	mu sync.RWMutex

	// infraByKey holds the latest pb.Infra per gateway key ("namespace/name").
	// Populated when upstream InfraIR changes. Do not use presence as a reconcile
	// gate — use knownKeys instead.
	infraByKey map[string]*ir.Infra

	// knownKeys is the IR-independent set of gateway keys this controller manages.
	// Populated by SetInfra and tag-based discovery on startup.
	knownKeys map[string]struct{}

	// elbproxyProxiesByKey holds the latest ELBProxyConfiguration CRD per "namespace/name" key.
	// Keyed by namespace/name because GatewayClass-scoped targetRefs allow proxies
	// in different namespaces to coexist with the same name.
	// Populated by the ELBProxyConfiguration CRD watcher.
	elbproxyProxiesByKey map[string]*elbproxyv1alpha1.ELBProxyConfiguration

	// xdsHosts holds the current VPC-routable IPs of all ready xDS server pods.
	// Populated by the EndpointSlice watcher.
	xdsHosts []string

	// backendPortsByKey holds the latest backend target ports per gateway key.
	// Populated by the route watcher.
	backendPortsByKey map[string][]ir.PortProtocol

	// gatewayClassByKey caches gateway key → GatewayClass name.
	// Populated on first resolve
	gatewayClassByKey map[string]string

	// gatewaysByClass is the reverse of gatewayClassByKey.
	// Used to fan out GatewayClass-scoped ELBProxyConfiguration changes to affected gateways.
	gatewaysByClass map[string]map[string]struct{}

	// effectiveProxyByGateway caches gateway key → attached ELBProxyConfiguration key.
	// Empty string means no ELBProxyConfiguration attaches.
	effectiveProxyByGateway map[string]string

	// gatewaysByEffectiveProxy is the reverse of effectiveProxyByGateway.
	// Used to find gateways affected by an ELBProxyConfiguration change without a full scan.
	gatewaysByEffectiveProxy map[string]map[string]struct{}

	// pendingDeletion tracks keys that are awaiting infrastructure teardown.
	// Set by MarkPendingDeletion when a delete request arrives; cleared by
	// ForgetKey after successful cleanup. Survives across requeues but NOT
	// across pod restarts (in-memory only — GC handles that case).
	pendingDeletion map[string]struct{}
}

// New returns an initialised Cache.
func New() *Cache {
	return &Cache{
		infraByKey:               make(map[string]*ir.Infra),
		knownKeys:                make(map[string]struct{}),
		pendingDeletion:          make(map[string]struct{}),
		elbproxyProxiesByKey:     make(map[string]*elbproxyv1alpha1.ELBProxyConfiguration),
		backendPortsByKey:        make(map[string][]ir.PortProtocol),
		gatewayClassByKey:        make(map[string]string),
		gatewaysByClass:          make(map[string]map[string]struct{}),
		effectiveProxyByGateway:  make(map[string]string),
		gatewaysByEffectiveProxy: make(map[string]map[string]struct{}),
	}
}

// SetInfra stores the pb.Infra for a gateway key and marks it known.
// A push means the gateway exists again, so it clears pendingDeletion: the controller stops
// tearing down and reconciles toward the current state.
func (c *Cache) SetInfra(key string, infra *ir.Infra) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.infraByKey[key] = infra
	c.knownKeys[key] = struct{}{}
	delete(c.pendingDeletion, key)
}

// DeleteInfra removes the pb.Infra for a gateway key and clears associated
// caches. Does NOT remove from knownKeys — use ForgetKey for full teardown.
func (c *Cache) DeleteInfra(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.infraByKey, key)
	delete(c.backendPortsByKey, key)
	c.clearGatewayClassLocked(key)
	c.clearEffectiveProxyLocked(key)
}

// GetInfra returns the cached pb.Infra for a gateway key.
func (c *Cache) GetInfra(key string) (*ir.Infra, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.infraByKey[key]
	return v, ok
}

// AllInfraKeys returns all gateway keys currently in the cache.
func (c *Cache) AllInfraKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.infraByKey))
	for k := range c.infraByKey {
		keys = append(keys, k)
	}
	return keys
}

// AddKnownKey records a gateway key as managed, independent of IR presence.
func (c *Cache) AddKnownKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.knownKeys[key] = struct{}{}
}

// IsKnownKey reports whether the key is managed by this controller.
func (c *Cache) IsKnownKey(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.knownKeys[key]
	return ok
}

// AllKnownKeys returns every managed gateway key, regardless of IR presence.
func (c *Cache) AllKnownKeys() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	keys := make([]string, 0, len(c.knownKeys))
	for k := range c.knownKeys {
		keys = append(keys, k)
	}
	return keys
}

// ForgetKey removes a key entirely (IR + known + pendingDeletion). Called after
// successful teardown to fully clear the key from the cache.
func (c *Cache) ForgetKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.infraByKey, key)
	delete(c.backendPortsByKey, key)
	delete(c.knownKeys, key)
	delete(c.pendingDeletion, key)
	c.clearGatewayClassLocked(key)
	c.clearEffectiveProxyLocked(key)
}

// ForgetKeyIfPendingDeletion forgets the key only if it is still pending deletion,
// so a SetInfra that arrived during teardown is not wiped. Reports whether it forgot.
func (c *Cache) ForgetKeyIfPendingDeletion(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.pendingDeletion[key]; !ok {
		return false
	}
	delete(c.infraByKey, key)
	delete(c.backendPortsByKey, key)
	delete(c.knownKeys, key)
	delete(c.pendingDeletion, key)
	c.clearGatewayClassLocked(key)
	c.clearEffectiveProxyLocked(key)
	return true
}

// MarkPendingDeletion marks a key for deletion and clears its IR/ports/associations.
// The key is kept in pendingDeletion so ReconcileKey can drive the teardown with
// controller-runtime retry and backoff.
func (c *Cache) MarkPendingDeletion(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.infraByKey, key)
	delete(c.backendPortsByKey, key)
	delete(c.knownKeys, key)
	c.pendingDeletion[key] = struct{}{}
	c.clearGatewayClassLocked(key)
	c.clearEffectiveProxyLocked(key)
}

// IsPendingDeletion reports whether the key is awaiting infrastructure teardown.
func (c *Cache) IsPendingDeletion(key string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.pendingDeletion[key]
	return ok
}

// HasPendingDeletionsExcluding reports whether any keys other than the excluded ones
// are awaiting infrastructure teardown.
func (c *Cache) HasPendingDeletionsExcluding(excludeKeys []string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.pendingDeletion) == 0 {
		return false
	}
	exclude := make(map[string]struct{}, len(excludeKeys))
	for _, k := range excludeKeys {
		exclude[k] = struct{}{}
	}
	for key := range c.pendingDeletion {
		if _, excluded := exclude[key]; !excluded {
			return true
		}
	}
	return false
}

// SetELBProxyConfiguration stores an ELBProxyConfiguration CRD keyed by "namespace/name".
func (c *Cache) SetELBProxyConfiguration(key string, proxy *elbproxyv1alpha1.ELBProxyConfiguration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.elbproxyProxiesByKey[key] = proxy
}

// DeleteELBProxyConfiguration removes an ELBProxyConfiguration CRD keyed by "namespace/name".
func (c *Cache) DeleteELBProxyConfiguration(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.elbproxyProxiesByKey, key)
}

// GetELBProxyConfiguration returns the cached ELBProxyConfiguration for a given "namespace/name" key.
func (c *Cache) GetELBProxyConfiguration(key string) (*elbproxyv1alpha1.ELBProxyConfiguration, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.elbproxyProxiesByKey[key]
	return v, ok
}

// AllELBProxyConfigurations returns a snapshot of all cached ELBProxyConfigurations.
func (c *Cache) AllELBProxyConfigurations() []*elbproxyv1alpha1.ELBProxyConfiguration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*elbproxyv1alpha1.ELBProxyConfiguration, 0, len(c.elbproxyProxiesByKey))
	for _, p := range c.elbproxyProxiesByKey {
		out = append(out, p)
	}
	return out
}

// SetXDSHosts updates the cached xDS server host addresses.
// Returns true if the value changed.
func (c *Cache) SetXDSHosts(hosts []string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.Equal(c.xdsHosts, hosts) {
		return false
	}
	c.xdsHosts = hosts
	return true
}

// XDSHosts returns the current xDS server host addresses.
func (c *Cache) XDSHosts() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.xdsHosts) == 0 {
		return nil
	}
	out := make([]string, len(c.xdsHosts))
	copy(out, c.xdsHosts)
	return out
}

// SetBackendPorts stores the backend target ports for a gateway key.
// Pass nil to remove the entry. Returns true if the value changed.
func (c *Cache) SetBackendPorts(key string, ports []ir.PortProtocol) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, exists := c.backendPortsByKey[key]
	if ports == nil {
		if !exists {
			return false
		}
		delete(c.backendPortsByKey, key)
		return true
	}
	// If the key doesn't exist yet, always store — even an empty slice is meaningful
	// as it signals "route watcher has run and found no ports" vs "not yet evaluated".
	if !exists {
		c.backendPortsByKey[key] = ports
		return true
	}
	existing := c.backendPortsByKey[key]
	if portsEqual(existing, ports) {
		return false
	}
	c.backendPortsByKey[key] = ports
	return true
}

// AllBackendPorts returns the union of all backend target ports across all active gateways.
// Only includes ports for gateways that are still present in infraByKey to avoid
// returning stale ports from the route
func (c *Cache) AllBackendPorts() []ir.PortProtocol {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[ir.PortProtocol]struct{})
	for key, ports := range c.backendPortsByKey {
		if _, active := c.infraByKey[key]; !active {
			continue
		}
		for _, p := range ports {
			seen[p] = struct{}{}
		}
	}
	out := make([]ir.PortProtocol, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

// GetBackendPorts returns the cached backend target ports for a gateway key.
// Returns nil if no entry exists, empty slice if explicitly cleared.
func (c *Cache) GetBackendPorts(key string) []ir.PortProtocol {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.backendPortsByKey[key]
}

// portsEqual returns true if two port slices contain the same entries regardless of order.
func portsEqual(a, b []ir.PortProtocol) bool {
	if len(a) != len(b) {
		return false
	}
	cmpFn := func(x, y ir.PortProtocol) int {
		if n := cmp.Compare(x.Port, y.Port); n != 0 {
			return n
		}
		return cmp.Compare(x.Protocol, y.Protocol)
	}
	ac := slices.SortedFunc(slices.Values(a), cmpFn)
	bc := slices.SortedFunc(slices.Values(b), cmpFn)
	return slices.Equal(ac, bc)
}

// SetGatewayClass stores the GatewayClass name for a gateway key and updates the reverse index.
// Empty className clears any prior entry without adding a new one.
func (c *Cache) SetGatewayClass(gatewayKey, className string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.gatewayClassByKey[gatewayKey]; ok && prev == className {
		return
	}
	c.clearGatewayClassLocked(gatewayKey)
	c.gatewayClassByKey[gatewayKey] = className
	if className != "" {
		if c.gatewaysByClass[className] == nil {
			c.gatewaysByClass[className] = make(map[string]struct{})
		}
		c.gatewaysByClass[className][gatewayKey] = struct{}{}
	}
}

// GetGatewayClass returns the cached GatewayClass name for a gateway key.
// ok is false when the key is absent or was set to empty.
func (c *Cache) GetGatewayClass(gatewayKey string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.gatewayClassByKey[gatewayKey]
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// GatewaysInClass returns all gateway keys in the given GatewayClass.
func (c *Cache) GatewaysInClass(className string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := c.gatewaysByClass[className]
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// SetEffectiveProxy stores the attached ELBProxyConfiguration key for a gateway and updates the reverse index.
// Empty proxyKey clears any prior entry without adding a new one.
func (c *Cache) SetEffectiveProxy(gatewayKey, proxyKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.effectiveProxyByGateway[gatewayKey]; ok && prev == proxyKey {
		return
	}
	c.clearEffectiveProxyLocked(gatewayKey)
	c.effectiveProxyByGateway[gatewayKey] = proxyKey
	if proxyKey != "" {
		if c.gatewaysByEffectiveProxy[proxyKey] == nil {
			c.gatewaysByEffectiveProxy[proxyKey] = make(map[string]struct{})
		}
		c.gatewaysByEffectiveProxy[proxyKey][gatewayKey] = struct{}{}
	}
}

// GetEffectiveProxy returns the ELBProxyConfiguration key attached to a gateway.
// ok is false when the key is absent or was set to empty.
func (c *Cache) GetEffectiveProxy(gatewayKey string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.effectiveProxyByGateway[gatewayKey]
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// GatewaysForEffectiveProxy returns all gateway keys attached to the given ELBProxyConfiguration key.
func (c *Cache) GatewaysForEffectiveProxy(proxyKey string) []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	m := c.gatewaysByEffectiveProxy[proxyKey]
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// clearGatewayClassLocked removes the gateway's class entry and its reverse mapping.
// Caller must hold c.mu for writing.
func (c *Cache) clearGatewayClassLocked(gatewayKey string) {
	prev, ok := c.gatewayClassByKey[gatewayKey]
	if !ok {
		return
	}
	delete(c.gatewayClassByKey, gatewayKey)
	if prev == "" {
		return
	}
	if set := c.gatewaysByClass[prev]; set != nil {
		delete(set, gatewayKey)
		if len(set) == 0 {
			delete(c.gatewaysByClass, prev)
		}
	}
}

// clearEffectiveProxyLocked removes the gateway's effective-proxy entry and its reverse mapping.
// Caller must hold c.mu for writing.
func (c *Cache) clearEffectiveProxyLocked(gatewayKey string) {
	prev, ok := c.effectiveProxyByGateway[gatewayKey]
	if !ok {
		return
	}
	delete(c.effectiveProxyByGateway, gatewayKey)
	if prev == "" {
		return
	}
	if set := c.gatewaysByEffectiveProxy[prev]; set != nil {
		delete(set, gatewayKey)
		if len(set) == 0 {
			delete(c.gatewaysByEffectiveProxy, prev)
		}
	}
}
