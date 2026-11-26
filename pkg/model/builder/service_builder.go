// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/ir"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/model/core"
	elbproxymodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/elbproxy"
	kubemodel "github.com/aws/elb-envoy-remote-infra-provider/pkg/model/kubernetes"
	pb "github.com/envoyproxy/gateway/proto/remoteinfra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

// owningGatewayClassLabel is the label EG sets on merged-gateway IR metadata.
const (
	owningGatewayClassLabel     = "gateway.envoyproxy.io/owning-gatewayclass"
	owningGatewayNameLabel      = "gateway.envoyproxy.io/owning-gateway-name"
	owningGatewayNamespaceLabel = "gateway.envoyproxy.io/owning-gateway-namespace"
)

// ServiceBuilder builds a Kubernetes Service stack resource for an ELBProxy proxy.
type ServiceBuilder interface {
	Build(stack core.Stack, pr *elbproxymodel.ProxyResource, irInfra *ir.Infra) error
}

const (
	elbProxyClass = "aws.elbproxy.internal"

	// servicePrefix is the prefix used for generated Service names. The resulting
	// name is truncated to stay within Kubernetes' 63-character DNS label limit.
	servicePrefix = "elbp"
)

// invalidServiceNamePattern matches anything that isn't a lowercase letter, digit,
// or dash — Kubernetes Service names are RFC 1123 DNS labels.
var invalidServiceNamePattern = regexp.MustCompile("[^a-z0-9-]")

type serviceBuilder struct{}

var _ ServiceBuilder = &serviceBuilder{}

func newServiceBuilder() ServiceBuilder {
	return &serviceBuilder{}
}

func (s *serviceBuilder) Build(stack core.Stack, pr *elbproxymodel.ProxyResource, irInfra *ir.Infra) error {
	if pr == nil {
		return errors.New("elbproxy proxy resource is nil")
	}
	if irInfra == nil {
		return errors.New("ir infrastructure is nil")
	}
	proxyInfra := irInfra.GetProxyInfra()

	svcName, svcLabels := s.buildServiceNameAndLabels(proxyInfra)

	svcPorts, err := buildServicePorts(proxyInfra)
	if err != nil {
		return fmt.Errorf("failed to build service ports: %w", err)
	}

	svcSpec := corev1.ServiceSpec{
		Type:                          corev1.ServiceTypeLoadBalancer,
		LoadBalancerClass:             ptr.To(elbProxyClass),
		AllocateLoadBalancerNodePorts: ptr.To(false),
		Ports:                         svcPorts,
	}

	status := &kubemodel.ServiceStatus{
		ELBProxyEndpoint: pr.ProxyDNS(),
	}

	resSpec := kubemodel.ServiceResourceSpec{
		Template: kubemodel.ServiceTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      svcName,
				Namespace: config.EnvoyGatewayNamespace(),
				Labels:    svcLabels,
			},
			Spec: svcSpec,
		},
	}

	kubemodel.NewServiceResource(stack, pr.Name, resSpec, status)
	return nil
}

// buildServiceNameAndLabels returns the Service name and identity labels for the
// proxy. It handles two IR shapes:
//
//   - Non-merged: Proxy.Name is "namespace/gateway-name". The Service is named
//     after the gateway and carries per-gateway owning labels.
//
//   - Merged: Proxy.Name is the GatewayClass name (no "/") and
//     Proxy.Metadata.Labels carries "gateway.envoyproxy.io/owning-gatewayclass".
//     The Service is named after the GatewayClass and carries the owning-gatewayclass
//     label so EG can find it via its selector.
func (s *serviceBuilder) buildServiceNameAndLabels(proxyInfra *pb.ProxyInfra) (string, map[string]string) {
	labels := s.generateServiceLabels(proxyInfra.Metadata)
	name := buildServiceName(labels)
	return name, labels
}

// generateServiceLabels returns per-gateway identity labels for non-merged gateways.
// Controller-owned stack labels are layered on at synthesize time by the ServiceManager.
func (s *serviceBuilder) generateServiceLabels(infraMetadata *pb.InfraMetadata) map[string]string {
	base := map[string]string{
		"app.kubernetes.io/component":     "proxy",
		"app.kubernetes.io/managed-by":    "envoy-gateway",
		"app.kubernetes.io/name":          "envoy",
		"gateway.envoyproxy.io/delegated": "elbproxy-extension-server",
	}

	if infraMetadata != nil {
		for k, v := range infraMetadata.Labels {
			base[k] = v
		}
	}

	return base
}

// buildServiceName produces a deterministic, DNS-safe Service name from a gateway
// namespace and name.
//
// Format: elbp-<sanitized-ns>-<sanitized-name>-<10-char-hash>
// Max length: 63 (Kubernetes DNS label limit).
func buildServiceName(labels map[string]string) string {
	gwClass, isMerged := labels[owningGatewayClassLabel]
	if isMerged {
		return buildServiceNameFromKey(gwClass)
	}

	return buildServiceNameFromKey(labels[owningGatewayNamespaceLabel] + "/" + labels[owningGatewayNameLabel])
}

// buildServiceNameFromKey produces a deterministic, DNS-safe Service name from an
// opaque key string (either "namespace/name" for non-merged or a GatewayClass name
// for merged gateways). The hash suffix guarantees uniqueness even when two keys
// sanitize down to the same prefix.
//
// Format: elbp-<sanitized-key>-<10-char-hash>
// Max length: 63 (Kubernetes DNS label limit).
func buildServiceNameFromKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	hash := hex.EncodeToString(sum[:])[:10]
	sanitized := invalidServiceNamePattern.ReplaceAllString(strings.ToLower(key), "")
	return fmt.Sprintf("%s-%.47s-%s", servicePrefix, sanitized, hash)
}

// buildServicePorts converts the proxy listener ports from the IR into Kubernetes
// Service ports. Kubernetes requires at least one port on a Service of type
// LoadBalancer, and each port name must be unique within the Service.
//
// Port naming: we prefer the listener port's name. Duplicate names (when multiple
// listeners declare a port with the same name) are disambiguated with a suffix.
// Unnamed ports fall back to `<protocol>-<servicePort>`.
func buildServicePorts(proxyInfra *pb.ProxyInfra) ([]corev1.ServicePort, error) {
	if proxyInfra == nil {
		return nil, errors.New("proxy infra is nil")
	}

	type portKey struct {
		servicePort int32
		protocol    corev1.Protocol
	}

	var ports []corev1.ServicePort
	seenPorts := make(map[portKey]bool)
	usedNames := make(map[string]int)

	for _, listener := range proxyInfra.Listeners {
		if listener == nil {
			continue
		}
		for _, port := range listener.Ports {
			protocol := listenerProtocolToServiceProtocol(port.Protocol)
			key := portKey{servicePort: port.ServicePort, protocol: protocol}
			if seenPorts[key] {
				continue
			}
			seenPorts[key] = true

			name := uniqueServicePortName(port, protocol, usedNames)
			ports = append(ports, corev1.ServicePort{
				Name:       name,
				Protocol:   protocol,
				Port:       port.ServicePort,
				TargetPort: intstr.FromInt32(port.ContainerPort),
			})
		}
	}

	if len(ports) == 0 {
		return nil, errors.New("proxy infra has no listener ports; at least one is required for a LoadBalancer Service")
	}
	return ports, nil
}

// listenerProtocolToServiceProtocol maps an IR listener protocol to a Kubernetes
// Service protocol. Kubernetes Service ports only understand TCP / UDP / SCTP, so
// application-level protocols (HTTP, HTTPS, TLS) are modeled as TCP.
func listenerProtocolToServiceProtocol(p string) corev1.Protocol {
	if p == ir.UDPProtocolType {
		return corev1.ProtocolUDP
	}
	return corev1.ProtocolTCP
}

// uniqueServicePortName returns a Service port name that is unique within a
// single Service and safe for the Kubernetes port-name constraints (15-character
// DNS-1123 label, lowercase alphanumerics and dashes, must not be all numeric).
func uniqueServicePortName(port *pb.ListenerPort, protocol corev1.Protocol, usedNames map[string]int) string {
	base := sanitizeServicePortName(port.Name)
	if base == "" {
		base = sanitizeServicePortName(fmt.Sprintf("%s-%d", strings.ToLower(string(protocol)), port.ServicePort))
	}
	name := base
	if count, exists := usedNames[base]; exists {
		name = sanitizeServicePortName(fmt.Sprintf("%s-%d", base, count+1))
		usedNames[base] = count + 1
	} else {
		usedNames[base] = 1
	}
	return name
}

// invalidServicePortNameCharPattern matches characters that are not allowed in
// a Kubernetes Service port name (DNS-1123 labels). Port names must be lowercase
// alphanumerics or '-'.
var invalidServicePortNameCharPattern = regexp.MustCompile(`[^a-z0-9-]`)

// servicePortNameEdgePattern matches non-alphanumerics at the start or end of
// a port name (port names must start and end with an alphanumeric character).
var servicePortNameEdgePattern = regexp.MustCompile(`^[^a-z0-9]+|[^a-z0-9]+$`)

// sanitizeServicePortName coerces an arbitrary string into a valid Kubernetes
// Service port name: lowercased, 15 characters max, only [a-z0-9-], and
// starting/ending with an alphanumeric.
func sanitizeServicePortName(name string) string {
	lowered := strings.ToLower(name)
	sanitized := invalidServicePortNameCharPattern.ReplaceAllString(lowered, "-")
	if len(sanitized) > 15 {
		sanitized = sanitized[:15]
	}
	return servicePortNameEdgePattern.ReplaceAllString(sanitized, "")
}
