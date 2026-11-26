// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package resources

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/ptr"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
	gwapiv1a2 "sigs.k8s.io/gateway-api/apis/v1alpha2"

	elbproxyv1alpha1 "github.com/aws/elb-envoy-remote-infra-provider/pkg/api/v1alpha1"
	"github.com/aws/elb-envoy-remote-infra-provider/test/e2e/framework"
)

// BuildEchoDeploymentSpec builds the HTTP echo backend (Gateway API echo-basic).
func BuildEchoDeploymentSpec() *appsv1.Deployment {
	return buildDeployment(EchoName, corev1.Container{
		Name:  "echo",
		Image: EchoImage,
		Env: []corev1.EnvVar{
			envFromField("POD_NAME", "metadata.name"),
			envFromField("NAMESPACE", "metadata.namespace"),
			{Name: "SERVICE_NAME", Value: EchoName},
		},
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: EchoContainerPort}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(EchoContainerPort)},
			},
			InitialDelaySeconds: 1,
			PeriodSeconds:       2,
		},
		Resources: smallRequests("32Mi"),
	})
}

// BuildEchoServiceSpec builds the Service in front of the echo backend.
func BuildEchoServiceSpec() *corev1.Service {
	return buildService(EchoName, corev1.ServicePort{
		Name:       "http",
		Protocol:   corev1.ProtocolTCP,
		Port:       EchoServicePort,
		TargetPort: intstr.FromInt32(EchoContainerPort),
	})
}

// BuildEchoDeploymentOnPort builds an echo backend named name on port.
func BuildEchoDeploymentOnPort(name string, port int32) *appsv1.Deployment {
	return buildDeployment(name, corev1.Container{
		Name:  "echo",
		Image: EchoImage,
		Env: []corev1.EnvVar{
			{Name: "HTTP_PORT", Value: strconv.Itoa(int(port))},
			{Name: "SERVICE_NAME", Value: name},
		},
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(port)},
			},
			InitialDelaySeconds: 1,
			PeriodSeconds:       2,
		},
		Resources: smallRequests("32Mi"),
	})
}

// BuildEchoServiceOnPort builds the Service for a BuildEchoDeploymentOnPort backend:
// EchoServicePort, targeting port.
func BuildEchoServiceOnPort(name string, port int32) *corev1.Service {
	return buildService(name, corev1.ServicePort{
		Name:       "http",
		Protocol:   corev1.ProtocolTCP,
		Port:       EchoServicePort,
		TargetPort: intstr.FromInt32(port),
	})
}

// BuildExtAuthDeploymentSpec builds the HTTP ext_authz backend: echo-basic on
// ExtAuthPort (see ExtAuthPathPrefix for how it decides).
func BuildExtAuthDeploymentSpec() *appsv1.Deployment {
	return BuildExtAuthDeploymentOnPort(ExtAuthName, ExtAuthPort)
}

// BuildExtAuthDeploymentOnPort builds an ext_authz backend named name on port.
func BuildExtAuthDeploymentOnPort(name string, port int32) *appsv1.Deployment {
	return buildDeployment(name, corev1.Container{
		Name:  "echo",
		Image: EchoImage,
		Env: []corev1.EnvVar{
			{Name: "HTTP_PORT", Value: strconv.Itoa(int(port))},
			{Name: "SERVICE_NAME", Value: name},
		},
		Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: port}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(port)},
			},
			InitialDelaySeconds: 1,
			PeriodSeconds:       2,
		},
		Resources: smallRequests("32Mi"),
	})
}

// BuildExtAuthServiceSpec builds the Service in front of the ext_authz backend.
func BuildExtAuthServiceSpec() *corev1.Service {
	return BuildExtAuthServiceOnPort(ExtAuthName, ExtAuthPort)
}

// BuildExtAuthServiceOnPort builds the Service for a BuildExtAuthDeploymentOnPort backend.
func BuildExtAuthServiceOnPort(name string, port int32) *corev1.Service {
	return buildService(name, corev1.ServicePort{
		Name:       "http",
		Protocol:   corev1.ProtocolTCP,
		Port:       port,
		TargetPort: intstr.FromInt32(port),
	})
}

// BuildExtAuthSecurityPolicy builds a SecurityPolicy that sends every request on
// the HTTPRoute routeName to the ext_authz backend (see BuildExtAuthDeploymentSpec).
// Envoy prepends ExtAuthPathPrefix to the request path of each auth request, so a
// request for /401 is checked against /status/401.
// It is unstructured, like the EnvoyProxy, and has no namespace, so Deploy places it
// in the stack's namespace next to the route.
func BuildExtAuthSecurityPolicy(routeName string) *unstructured.Unstructured {
	return buildExtAuthSecurityPolicy(routeName+"-ext-auth", map[string]any{
		"targetRefs": []any{map[string]any{
			"group": gwapiv1.GroupName,
			"kind":  "HTTPRoute",
			"name":  routeName,
		}},
	}, ExtAuthName, ExtAuthPort)
}

// BuildExtAuthSecurityPolicyForSelector builds an ext_authz SecurityPolicy that selects
// HTTPRoutes with routeLabels, using the backend backendName on port.
func BuildExtAuthSecurityPolicyForSelector(name string, routeLabels map[string]string, backendName string, port int32) *unstructured.Unstructured {
	// Unstructured objects hold only JSON types.
	matchLabels := make(map[string]any, len(routeLabels))
	for k, v := range routeLabels {
		matchLabels[k] = v
	}
	return buildExtAuthSecurityPolicy(name, map[string]any{
		"targetSelectors": []any{map[string]any{
			"group":       gwapiv1.GroupName,
			"kind":        "HTTPRoute",
			"matchLabels": matchLabels,
		}},
	}, backendName, port)
}

// BuildExtAuthSecurityPolicyForListenerSet builds an ext_authz SecurityPolicy that
// targets the ListenerSet listenerSetName, using the backend backendName on port.
func BuildExtAuthSecurityPolicyForListenerSet(listenerSetName, backendName string, port int32) *unstructured.Unstructured {
	return buildExtAuthSecurityPolicy(listenerSetName+"-ext-auth", map[string]any{
		"targetRefs": []any{map[string]any{
			"group": gwapiv1.GroupName,
			"kind":  "ListenerSet",
			"name":  listenerSetName,
		}},
	}, backendName, port)
}

// BuildHTTP3ClientTrafficPolicy builds a ClientTrafficPolicy that turns on HTTP/3 for
// the HTTPS listeners of the Gateway gatewayName.
func BuildHTTP3ClientTrafficPolicy(gatewayName string) *unstructured.Unstructured {
	ctp := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"targetRefs": []any{map[string]any{
				"group": gwapiv1.GroupName,
				"kind":  "Gateway",
				"name":  gatewayName,
			}},
			"http3": map[string]any{},
		},
	}}
	ctp.SetGroupVersionKind(ClientTrafficPolicyGVK)
	ctp.SetName(gatewayName + "-http3")
	return ctp
}

// buildExtAuthSecurityPolicy builds an ext_authz SecurityPolicy with attachment as its
// targetRefs or targetSelectors.
func buildExtAuthSecurityPolicy(name string, attachment map[string]any, backendName string, port int32) *unstructured.Unstructured {
	spec := map[string]any{
		"extAuth": map[string]any{
			"http": map[string]any{
				"backendRefs": []any{map[string]any{
					"name": backendName,
					"port": int64(port),
				}},
				"path": ExtAuthPathPrefix,
			},
		},
	}
	for k, v := range attachment {
		spec[k] = v
	}
	sp := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	sp.SetGroupVersionKind(SecurityPolicyGVK)
	sp.SetName(name)
	return sp
}

// BuildGRPCDeploymentSpec builds the gRPC echo backend.
func BuildGRPCDeploymentSpec() *appsv1.Deployment {
	return buildDeployment(GRPCName, corev1.Container{
		Name:  "grpc-echo",
		Image: GRPCImage,
		Ports: []corev1.ContainerPort{{Name: "grpc", ContainerPort: GRPCContainerPort}},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler:        corev1.ProbeHandler{GRPC: &corev1.GRPCAction{Port: GRPCContainerPort}},
			InitialDelaySeconds: 2,
			PeriodSeconds:       3,
		},
		Resources: smallRequests("32Mi"),
	})
}

// BuildGRPCServiceSpec builds the Service in front of the gRPC backend. The
// h2c appProtocol makes Envoy speak cleartext HTTP/2 to it.
func BuildGRPCServiceSpec() *corev1.Service {
	return buildService(GRPCName, corev1.ServicePort{
		Name:        "grpc",
		Protocol:    corev1.ProtocolTCP,
		Port:        GRPCServicePort,
		TargetPort:  intstr.FromInt32(GRPCContainerPort),
		AppProtocol: ptr.To("kubernetes.io/h2c"),
	})
}

// BuildUDPDeploymentSpec builds the UDP echo backend, which sends every
// datagram back to its sender.
func BuildUDPDeploymentSpec() *appsv1.Deployment {
	return buildDeployment(UDPName, corev1.Container{
		Name:    "udp-echo",
		Image:   UDPImage,
		Command: []string{"sh", "-c"},
		// Bind dual-stack ([::] with ipv6only=0) so the echo works on both IPv4
		// and IPv6 pod-network clusters. UDP4-RECVFROM binds IPv4 only, which
		// breaks on IPv6 clusters where the pod endpoint the proxy targets is IPv6.
		Args: []string{fmt.Sprintf(
			"apk add --no-cache socat && exec socat UDP6-RECVFROM:%d,ipv6only=0,fork EXEC:cat", UDPPort)},
		Ports:     []corev1.ContainerPort{{Name: "udp", ContainerPort: UDPPort, Protocol: corev1.ProtocolUDP}},
		Resources: smallRequests("16Mi"),
	})
}

// BuildUDPServiceSpec builds the Service in front of the UDP backend.
func BuildUDPServiceSpec() *corev1.Service {
	return buildService(UDPName, corev1.ServicePort{
		Name:       "udp",
		Protocol:   corev1.ProtocolUDP,
		Port:       UDPPort,
		TargetPort: intstr.FromInt32(UDPPort),
	})
}

// BuildTLSBackendDeploymentSpec builds an nginx backend that terminates TLS
// itself, using the certificate in TLSCertSecretName (see BuildTLSSecret) and
// the config from BuildNginxTLSConfigMap.
func BuildTLSBackendDeploymentSpec() *appsv1.Deployment {
	dp := buildDeployment(TLSBackendName, corev1.Container{
		Name:  "nginx",
		Image: TLSBackendImage,
		Ports: []corev1.ContainerPort{{Name: "tls", ContainerPort: TLSBackendPort}},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "tls-cert", MountPath: "/etc/nginx/ssl", ReadOnly: true},
			{Name: "nginx-conf", MountPath: "/etc/nginx/conf.d", ReadOnly: true},
		},
		Resources: smallRequests("32Mi"),
	})
	dp.Spec.Template.Spec.Volumes = []corev1.Volume{
		{
			Name:         "tls-cert",
			VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: TLSCertSecretName}},
		},
		{
			Name: "nginx-conf",
			VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: NginxConfName},
			}},
		},
	}
	return dp
}

// BuildTLSBackendServiceSpec builds the Service in front of the TLS backend.
func BuildTLSBackendServiceSpec() *corev1.Service {
	return buildService(TLSBackendName, corev1.ServicePort{
		Name:       "tls",
		Protocol:   corev1.ProtocolTCP,
		Port:       TLSBackendPort,
		TargetPort: intstr.FromInt32(TLSBackendPort),
	})
}

// BuildNginxTLSConfigMap builds the nginx config the TLS backend mounts.
func BuildNginxTLSConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: NginxConfName},
		Data:       map[string]string{"default.conf": nginxTLSConf},
	}
}

// BuildTLSSecret builds a kubernetes.io/tls Secret named TLSCertSecretName
// holding a new self-signed certificate for dnsNames. The TLS probes skip
// verification, so the certificate only has to be well formed; generating it
// per test keeps private keys out of the repo.
func BuildTLSSecret(dnsNames ...string) (*corev1.Secret, error) {
	if len(dnsNames) == 0 {
		return nil, errors.New("BuildTLSSecret: at least one DNS name is required")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		DNSNames:     dnsNames,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("marshal TLS key: %w", err)
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TLSCertSecretName},
		Type:       corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER}),
			corev1.TLSPrivateKeyKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		},
	}, nil
}

// BuildEnvoyProxySpec builds the EnvoyProxy for one test's GatewayClass. It
// lives in the Envoy Gateway namespace with a random name, so no two tests
// share one. Access logging is disabled because the ELBProxy controller rejects
// Envoy Gateway's default stdout file sink, and spec.ipFamily follows the
// cluster (see framework.Suite.EnvoyIPFamily). With mergeGateways, all Gateways
// in the class share one proxy fleet.
func BuildEnvoyProxySpec(s *framework.Suite, mergeGateways bool) *unstructured.Unstructured {
	spec := map[string]any{
		"ipFamily": s.EnvoyIPFamily(),
		"telemetry": map[string]any{
			"accessLog": map[string]any{"disable": true},
		},
	}
	if mergeGateways {
		spec["mergeGateways"] = true
	}
	ep := &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
	ep.SetGroupVersionKind(EnvoyProxyGVK)
	ep.SetNamespace(s.EnvoyProxyNamespace)
	ep.SetName(randomName())
	return ep
}

// BuildWasmEnvoyExtensionPolicy builds an EnvoyExtensionPolicy that runs the Wasm
// example module (see WasmModuleURL) on the HTTPRoute routeName. Envoy Gateway serves
// the module from its own pods, and the proxy downloads it through wasm_cluster.
// It is unstructured, like the EnvoyProxy, and has no namespace, so Deploy places it
// in the stack's namespace next to the route.
func BuildWasmEnvoyExtensionPolicy(routeName string) *unstructured.Unstructured {
	p := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"targetRefs": []any{map[string]any{
				"group": gwapiv1.GroupName,
				"kind":  "HTTPRoute",
				"name":  routeName,
			}},
			"wasm": []any{map[string]any{
				"name":   "wasm-example",
				"rootID": "my_root_id",
				"code": map[string]any{
					"type": "HTTP",
					"http": map[string]any{
						"url":    WasmModuleURL,
						"sha256": WasmModuleSHA256,
					},
				},
			}},
		},
	}}
	p.SetGroupVersionKind(EnvoyExtensionPolicyGVK)
	p.SetName(routeName + "-wasm")
	return p
}

// BuildGatewayClassSpec builds a GatewayClass for one test that points at
// envoyProxy through parametersRef. It reuses the EnvoyProxy's random name, so
// the pair is easy to match up.
func BuildGatewayClassSpec(s *framework.Suite, envoyProxy *unstructured.Unstructured) *gwapiv1.GatewayClass {
	return &gwapiv1.GatewayClass{
		ObjectMeta: metav1.ObjectMeta{Name: envoyProxy.GetName()},
		Spec: gwapiv1.GatewayClassSpec{
			ControllerName: gwapiv1.GatewayController(s.ControllerName),
			ParametersRef: &gwapiv1.ParametersReference{
				Group:     gwapiv1.Group(EnvoyProxyGVK.Group),
				Kind:      gwapiv1.Kind(EnvoyProxyGVK.Kind),
				Name:      envoyProxy.GetName(),
				Namespace: ptr.To(gwapiv1.Namespace(envoyProxy.GetNamespace())),
			},
		},
	}
}

// BuildGatewaySpec builds a Gateway in gwc with gwListeners. Routes built here
// attach to it by its name, DefaultGatewayName; rename it when a stack needs a
// second Gateway.
func BuildGatewaySpec(gwc *gwapiv1.GatewayClass, gwListeners []gwapiv1.Listener) *gwapiv1.Gateway {
	return &gwapiv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultGatewayName},
		Spec: gwapiv1.GatewaySpec{
			GatewayClassName: gwapiv1.ObjectName(gwc.Name),
			Listeners:        gwListeners,
		},
	}
}

// BuildHTTPRoute builds an HTTPRoute on the default Gateway, or only on its
// sectionName listener when set. With no rules, it routes DefaultPathPrefix to
// the echo backend.
func BuildHTTPRoute(hostnames []string, rules []gwapiv1.HTTPRouteRule, sectionName *gwapiv1.SectionName) *gwapiv1.HTTPRoute {
	if len(rules) == 0 {
		rules = []gwapiv1.HTTPRouteRule{BuildHTTPRouteRule(DefaultPathPrefix)}
	}
	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName("httproute")},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: defaultGatewayParent(sectionName),
			Hostnames:       toHostnames(hostnames),
			Rules:           rules,
		},
	}
}

// BuildHTTPRouteRule builds a rule that routes paths under pathPrefix to the
// echo backend.
func BuildHTTPRouteRule(pathPrefix string) gwapiv1.HTTPRouteRule {
	return BuildHTTPRouteRuleTo(pathPrefix, EchoName, EchoServicePort)
}

// BuildHTTPRouteRuleTo routes paths under pathPrefix to the Service backendName on port.
func BuildHTTPRouteRuleTo(pathPrefix, backendName string, port int32) gwapiv1.HTTPRouteRule {
	return gwapiv1.HTTPRouteRule{
		Matches: []gwapiv1.HTTPRouteMatch{{
			Path: &gwapiv1.HTTPPathMatch{
				Type:  ptr.To(gwapiv1.PathMatchPathPrefix),
				Value: ptr.To(pathPrefix),
			},
		}},
		BackendRefs: []gwapiv1.HTTPBackendRef{{BackendRef: BuildBackendRef(backendName, port)}},
	}
}

// BuildHTTPRouteRuleWithMirror is BuildHTTPRouteRule plus a mirror to the Service
// mirrorName on port.
func BuildHTTPRouteRuleWithMirror(pathPrefix, mirrorName string, port int32) gwapiv1.HTTPRouteRule {
	rule := BuildHTTPRouteRule(pathPrefix)
	rule.Filters = []gwapiv1.HTTPRouteFilter{{
		Type: gwapiv1.HTTPRouteFilterRequestMirror,
		RequestMirror: &gwapiv1.HTTPRequestMirrorFilter{
			BackendRef: gwapiv1.BackendObjectReference{
				Name: gwapiv1.ObjectName(mirrorName),
				Port: ptr.To(gwapiv1.PortNumber(port)),
			},
		},
	}}
	return rule
}

// BuildListenerSet builds a ListenerSet on the default Gateway with one HTTP listener on
// port. The Gateway must allow it in spec.allowedListeners.
func BuildListenerSet(port gwapiv1.PortNumber) *gwapiv1.ListenerSet {
	return &gwapiv1.ListenerSet{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultListenerSetName},
		Spec: gwapiv1.ListenerSetSpec{
			ParentRef: gwapiv1.ParentGatewayReference{Name: DefaultGatewayName},
			Listeners: []gwapiv1.ListenerEntry{{Name: "http", Protocol: gwapiv1.HTTPProtocolType, Port: port}},
		},
	}
}

// BuildHTTPRouteOnListenerSet builds an HTTPRoute attached to the ListenerSet
// listenerSetName.
func BuildHTTPRouteOnListenerSet(listenerSetName string, rules []gwapiv1.HTTPRouteRule) *gwapiv1.HTTPRoute {
	return &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName("httproute")},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{
					Group: ptr.To(gwapiv1.Group(gwapiv1.GroupName)),
					Kind:  ptr.To(gwapiv1.Kind("ListenerSet")),
					Name:  gwapiv1.ObjectName(listenerSetName),
				}},
			},
			Rules: rules,
		},
	}
}

// BuildGRPCRoute builds a GRPCRoute on the default Gateway that sends every
// method to the gRPC backend.
func BuildGRPCRoute(sectionName *gwapiv1.SectionName) *gwapiv1.GRPCRoute {
	return &gwapiv1.GRPCRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName("grpcroute")},
		Spec: gwapiv1.GRPCRouteSpec{
			CommonRouteSpec: defaultGatewayParent(sectionName),
			Rules: []gwapiv1.GRPCRouteRule{{
				BackendRefs: []gwapiv1.GRPCBackendRef{{BackendRef: BuildBackendRef(GRPCName, GRPCServicePort)}},
			}},
		},
	}
}

// BuildTCPRoute builds a TCPRoute on the default Gateway to the echo backend.
func BuildTCPRoute(sectionName *gwapiv1.SectionName) *gwapiv1.TCPRoute {
	return &gwapiv1.TCPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName("tcproute")},
		Spec: gwapiv1.TCPRouteSpec{
			CommonRouteSpec: defaultGatewayParent(sectionName),
			Rules: []gwapiv1.TCPRouteRule{{
				BackendRefs: []gwapiv1.BackendRef{BuildBackendRef(EchoName, EchoServicePort)},
			}},
		},
	}
}

// BuildUDPRoute builds a UDPRoute on the default Gateway to the UDP backend.
func BuildUDPRoute(sectionName *gwapiv1.SectionName) *gwapiv1.UDPRoute {
	return &gwapiv1.UDPRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName("udproute")},
		Spec: gwapiv1.UDPRouteSpec{
			CommonRouteSpec: defaultGatewayParent(sectionName),
			Rules: []gwapiv1.UDPRouteRule{{
				BackendRefs: []gwapiv1.BackendRef{BuildBackendRef(UDPName, UDPPort)},
			}},
		},
	}
}

// BuildTLSRoute builds a TLSRoute on the default Gateway that sends hostnames
// to backendRef.
func BuildTLSRoute(hostnames []string, backendRef gwapiv1.BackendRef, sectionName *gwapiv1.SectionName) *gwapiv1.TLSRoute {
	return &gwapiv1.TLSRoute{
		ObjectMeta: metav1.ObjectMeta{Name: routeName("tlsroute")},
		Spec: gwapiv1.TLSRouteSpec{
			CommonRouteSpec: defaultGatewayParent(sectionName),
			Hostnames:       toHostnames(hostnames),
			Rules:           []gwapiv1.TLSRouteRule{{BackendRefs: []gwapiv1.BackendRef{backendRef}}},
		},
	}
}

// BuildBackendRef references port of the Service name.
func BuildBackendRef(name string, port int32) gwapiv1.BackendRef {
	return gwapiv1.BackendRef{
		BackendObjectReference: gwapiv1.BackendObjectReference{
			Name: gwapiv1.ObjectName(name),
			Port: ptr.To(gwapiv1.PortNumber(port)),
		},
	}
}

// BuildELBProxyConfiguration builds a Gateway-scoped ELBProxyConfiguration
// for gw. It is deployed into the stack namespace, next to gw. Fields left
// empty in spec get the suite defaults (see withSuiteDefaults).
func BuildELBProxyConfiguration(s *framework.Suite, gw *gwapiv1.Gateway, spec elbproxyv1alpha1.ELBProxyConfigurationSpec) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: DefaultProxyName},
		Spec:       withSuiteDefaults(s, spec, "Gateway", gw.Name),
	}
}

// BuildGatewayClassELBProxyConfiguration builds a GatewayClass-scoped
// ELBProxyConfiguration for gwc. The controller only honors GatewayClass-scoped
// targetRefs from its own namespace, so it lives in s.EnvoyProxyNamespace with
// a random name. Fields left empty in spec get the suite defaults (see
// withSuiteDefaults).
func BuildGatewayClassELBProxyConfiguration(s *framework.Suite, gwc *gwapiv1.GatewayClass, spec elbproxyv1alpha1.ELBProxyConfigurationSpec) *elbproxyv1alpha1.ELBProxyConfiguration {
	return &elbproxyv1alpha1.ELBProxyConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: randomName(), Namespace: s.EnvoyProxyNamespace},
		Spec:       withSuiteDefaults(s, spec, "GatewayClass", gwc.Name),
	}
}

// withSuiteDefaults returns a copy of spec that targets targetKind/targetName,
// with the fields the test left empty taken from the suite flags: roleArn
// (-role-arn), subnets (-subnet-ids; none means
// controller auto-discovery), and IPv4/IPv6 addressability (-ip-family).
func withSuiteDefaults(s *framework.Suite, spec elbproxyv1alpha1.ELBProxyConfigurationSpec, targetKind gwapiv1.Kind, targetName string) elbproxyv1alpha1.ELBProxyConfigurationSpec {
	out := *spec.DeepCopy()
	out.TargetRefs = []gwapiv1a2.LocalPolicyTargetReference{{
		Group: gwapiv1.GroupName,
		Kind:  targetKind,
		Name:  gwapiv1.ObjectName(targetName),
	}}
	if out.RoleArn == "" {
		out.RoleArn = s.RoleArn
	}
	vpc := &out.NetworkConfiguration.VpcConfiguration
	if len(vpc.Subnets) == 0 {
		for _, id := range s.SubnetIDs {
			vpc.Subnets = append(vpc.Subnets, elbproxyv1alpha1.SubnetDetails{SubnetId: id})
		}
	}
	addressing := &out.NetworkConfiguration.IpConfiguration.IpAddressingConfiguration
	if addressing.Ipv4Addressability == "" {
		addressing.Ipv4Addressability = s.Ipv4Addressability
	}
	if addressing.Ipv6Addressability == "" {
		addressing.Ipv6Addressability = s.Ipv6Addressability
	}
	return out
}

// AllocateNamespace creates a namespace named DefaultName-baseName-<random>
// with labels and tracks it for cleanup. Create it before anything else in a
// stack so it's deleted last.
func AllocateNamespace(t *testing.T, s *framework.Suite, baseName string, labels map[string]string) *corev1.Namespace {
	t.Helper()
	const attempts = 3
	for i := 1; ; i++ {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("%s-%s-%s", DefaultName, baseName, utilrand.String(5)),
			Labels: labels,
		}}
		err := s.CreateObject(t, ns)
		if err == nil {
			return ns
		}
		// Retry only on a random-suffix collision.
		if !apierrors.IsAlreadyExists(err) || i == attempts {
			t.Fatalf("allocate namespace for %q: %v", baseName, err)
		}
	}
}

// randomName returns DefaultName-<random>, for objects that can't live in a
// stack's namespace (cluster-scoped, or in the shared Envoy Gateway namespace)
// and so need a unique name.
func randomName() string {
	return DefaultName + "-" + utilrand.String(5)
}

// routeName returns a unique route name, so a stack can hold several routes of
// one kind.
func routeName(kind string) string {
	return fmt.Sprintf("%s-%s-%s", DefaultName, kind, utilrand.String(5))
}

// defaultGatewayParent attaches a route to the default Gateway, or only to its
// sectionName listener when set.
func defaultGatewayParent(sectionName *gwapiv1.SectionName) gwapiv1.CommonRouteSpec {
	return gwapiv1.CommonRouteSpec{
		ParentRefs: []gwapiv1.ParentReference{{Name: DefaultGatewayName, SectionName: sectionName}},
	}
}

func toHostnames(hostnames []string) []gwapiv1.Hostname {
	if len(hostnames) == 0 {
		return nil
	}
	out := make([]gwapiv1.Hostname, 0, len(hostnames))
	for _, h := range hostnames {
		out = append(out, gwapiv1.Hostname(h))
	}
	return out
}

// buildDeployment builds a single-replica Deployment running container, with
// pods selected by app=name.
func buildDeployment(name string, container corev1.Container) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"app": name}},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec:       corev1.PodSpec{Containers: []corev1.Container{container}},
			},
		},
	}
}

// buildService builds a Service for the app=name pods.
func buildService(name string, port corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{port},
		},
	}
}

func envFromField(name, fieldPath string) corev1.EnvVar {
	return corev1.EnvVar{
		Name:      name,
		ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: fieldPath}},
	}
}

// smallRequests keeps test backends small enough to schedule anywhere.
func smallRequests(memory string) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse("10m"),
		corev1.ResourceMemory: resource.MustParse(memory),
	}}
}
