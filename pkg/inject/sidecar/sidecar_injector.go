// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package sidecar injects the elb-envoy-remote-infra-provider native sidecar
// into the Envoy Gateway control-plane pod. The sidecar is an initContainer with
// restartPolicy: Always (a Kubernetes native sidecar), sharing a Unix domain
// socket with the envoy-gateway container over an emptyDir volume.
package sidecar

import (
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"

	"github.com/aws/elb-envoy-remote-infra-provider/pkg/probe"
)

const (
	// SidecarContainerName is the name of the injected native sidecar initContainer.
	SidecarContainerName = "elb-envoy-remote-infra-provider"
	// SocketVolumeName is the shared emptyDir volume carrying the UDS socket.
	SocketVolumeName = "elb-envoy-remote-infra-provider-socket"
	// CertsVolumeName is the existing envoy-gateway TLS certs volume the sidecar reads.
	CertsVolumeName = "certs"

	// BinaryPath is the binary's path in the image. Exec probes call it, so
	// changing it breaks pods injected earlier.
	BinaryPath = "/usr/local/bin/elb-envoy-remote-infra-provider"

	// Config defaults.
	defaultSocketPath      = "/var/run/elb-envoy-remote-infra-provider/server.sock"
	defaultCertsMountPath  = "/tmp/certs"
	defaultEGContainerName = "envoy-gateway"

	// runAsUser / runAsGroup match the envoy-gateway container so the UDS socket
	// (created mode 0660) is accessible via owner/group permissions.
	runAsUserGroup int64 = 65532
	// fsGroup ensures the shared emptyDir is group-owned by 65532 so the non-root
	// sidecar can create the socket file in it.
	fsGroup int64 = 65532
)

// Config controls what the injector adds. Values come from the Helm chart
// rather than being hardcoded, so images/args/env are overridable.
type Config struct {
	Image           string
	ImagePullPolicy corev1.PullPolicy
	// SocketPath must match envoy-gateway's infrastructure.remote.service.unix.path.
	SocketPath string
	// SocketMountPath is the directory the socket lives in (parent of SocketPath).
	SocketMountPath string
	CertsMountPath  string
	// EnvoyGatewayContainerName is the control-plane container the shared socket
	// volumeMount is added to (and whose presence marks a pod as injectable).
	EnvoyGatewayContainerName string
	// ExtraArgs are appended after --server-socket-path.
	ExtraArgs  []string
	Env        []corev1.EnvVar
	Resources  corev1.ResourceRequirements
	MountCerts bool
}

// SidecarInjector injects the native sidecar into a pod. It is idempotent.
type SidecarInjector struct {
	cfg Config
}

// NewSidecarInjector returns a SidecarInjector with sensible defaults filled in.
func NewSidecarInjector(cfg Config) *SidecarInjector {
	if cfg.SocketPath == "" {
		cfg.SocketPath = defaultSocketPath
	}
	if cfg.SocketMountPath == "" {
		// Default the mount dir to the socket's parent so a custom SocketPath is
		// always backed by the shared volume.
		cfg.SocketMountPath = filepath.Dir(cfg.SocketPath)
	}
	if cfg.CertsMountPath == "" {
		cfg.CertsMountPath = defaultCertsMountPath
	}
	if cfg.EnvoyGatewayContainerName == "" {
		cfg.EnvoyGatewayContainerName = defaultEGContainerName
	}
	if cfg.ImagePullPolicy == "" {
		cfg.ImagePullPolicy = corev1.PullIfNotPresent
	}
	if cfg.Resources.Requests == nil && cfg.Resources.Limits == nil {
		cfg.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("100m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("128Mi"),
			},
		}
	}
	return &SidecarInjector{cfg: cfg}
}

// Inject mutates the pod in place, adding the sidecar initContainer, the shared
// socket volume, the pod fsGroup, and the socket volumeMount on the
// envoy-gateway container. It is a no-op if the sidecar is already present.
func (i *SidecarInjector) Inject(pod *corev1.Pod) {
	// Only mutate the Envoy Gateway control-plane pod
	if !i.hasEnvoyGatewayContainer(pod) {
		return
	}
	if i.alreadyInjected(pod) {
		return
	}

	i.ensureSocketVolume(pod)
	i.ensureFSGroup(pod)
	i.ensureSocketMountOnEnvoyGateway(pod)
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, i.buildSidecarContainer(pod))
}

func (i *SidecarInjector) hasEnvoyGatewayContainer(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == i.cfg.EnvoyGatewayContainerName {
			return true
		}
	}
	return false
}

func (i *SidecarInjector) hasCertsVolume(pod *corev1.Pod) bool {
	for _, v := range pod.Spec.Volumes {
		if v.Name == CertsVolumeName {
			return true
		}
	}
	return false
}

func (i *SidecarInjector) alreadyInjected(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.InitContainers {
		if c.Name == SidecarContainerName {
			return true
		}
	}
	return false
}

func (i *SidecarInjector) ensureSocketVolume(pod *corev1.Pod) {
	for _, v := range pod.Spec.Volumes {
		if v.Name == SocketVolumeName {
			return
		}
	}
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         SocketVolumeName,
		VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
	})
}

func (i *SidecarInjector) ensureFSGroup(pod *corev1.Pod) {
	if pod.Spec.SecurityContext == nil {
		pod.Spec.SecurityContext = &corev1.PodSecurityContext{}
	}
	if pod.Spec.SecurityContext.FSGroup == nil {
		pod.Spec.SecurityContext.FSGroup = ptr.To(fsGroup)
	}
}

func (i *SidecarInjector) ensureSocketMountOnEnvoyGateway(pod *corev1.Pod) {
	for idx := range pod.Spec.Containers {
		c := &pod.Spec.Containers[idx]
		if c.Name != i.cfg.EnvoyGatewayContainerName {
			continue
		}
		for _, vm := range c.VolumeMounts {
			if vm.Name == SocketVolumeName {
				return
			}
		}
		c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
			Name:      SocketVolumeName,
			MountPath: i.cfg.SocketMountPath,
		})
		return
	}
}

func (i *SidecarInjector) buildSidecarContainer(pod *corev1.Pod) corev1.Container {
	args := append([]string{"--server-socket-path=" + i.cfg.SocketPath}, i.cfg.ExtraArgs...)

	// Use our binary, not `test -S`: distroless has no shell utilities.
	socketProbe := func() *corev1.Probe {
		return &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				Exec: &corev1.ExecAction{Command: []string{BinaryPath, probe.Command, "--socket=" + i.cfg.SocketPath}},
			},
		}
	}

	volumeMounts := []corev1.VolumeMount{
		{Name: SocketVolumeName, MountPath: i.cfg.SocketMountPath},
	}

	if i.cfg.MountCerts && i.hasCertsVolume(pod) {
		volumeMounts = append(volumeMounts, corev1.VolumeMount{
			Name:      CertsVolumeName,
			MountPath: i.cfg.CertsMountPath,
			ReadOnly:  true,
		})
	}

	startup := socketProbe()
	startup.PeriodSeconds = 1
	startup.FailureThreshold = 30

	readiness := socketProbe()
	readiness.PeriodSeconds = 10

	liveness := socketProbe()
	liveness.InitialDelaySeconds = 10
	liveness.PeriodSeconds = 15

	return corev1.Container{
		Name:            SidecarContainerName,
		Image:           i.cfg.Image,
		ImagePullPolicy: i.cfg.ImagePullPolicy,
		RestartPolicy:   ptr.To(corev1.ContainerRestartPolicyAlways),
		Args:            args,
		Env:             i.cfg.Env,
		Resources:       i.cfg.Resources,
		SecurityContext: &corev1.SecurityContext{
			// Restricted Pod Security also checks init containers.
			AllowPrivilegeEscalation: ptr.To(false),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			Privileged:               ptr.To(false),
			RunAsNonRoot:             ptr.To(true),
			RunAsUser:                ptr.To(runAsUserGroup),
			RunAsGroup:               ptr.To(runAsUserGroup),
		},
		StartupProbe:   startup,
		ReadinessProbe: readiness,
		LivenessProbe:  liveness,
		VolumeMounts:   volumeMounts,
	}
}
