// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package sidecar

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func egPod() *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "envoy-gateway"},
			},
		},
	}
}

func findInitContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == name {
			return &pod.Spec.InitContainers[i]
		}
	}
	return nil
}

func findVolume(pod *corev1.Pod, name string) *corev1.Volume {
	for i := range pod.Spec.Volumes {
		if pod.Spec.Volumes[i].Name == name {
			return &pod.Spec.Volumes[i]
		}
	}
	return nil
}

func findContainer(pod *corev1.Pod, name string) *corev1.Container {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == name {
			return &pod.Spec.Containers[i]
		}
	}
	return nil
}

func hasMount(c *corev1.Container, name string) bool {
	if c == nil {
		return false
	}
	for _, vm := range c.VolumeMounts {
		if vm.Name == name {
			return true
		}
	}
	return false
}

func TestInject_AddsSidecarVolumeFSGroupAndMount(t *testing.T) {
	injector := NewSidecarInjector(Config{Image: "example.com/elb-envoy-remote-infra-provider:v1"})
	pod := egPod()

	injector.Inject(pod)

	// Native sidecar initContainer present and configured.
	sc := findInitContainer(pod, SidecarContainerName)
	if sc == nil {
		t.Fatalf("expected initContainer %q to be injected", SidecarContainerName)
	}
	if sc.Image != "example.com/elb-envoy-remote-infra-provider:v1" {
		t.Errorf("sidecar image = %q, want example.com/elb-envoy-remote-infra-provider:v1", sc.Image)
	}
	if sc.RestartPolicy == nil || *sc.RestartPolicy != corev1.ContainerRestartPolicyAlways {
		t.Errorf("sidecar RestartPolicy = %v, want Always (native sidecar)", sc.RestartPolicy)
	}
	if sc.SecurityContext == nil || sc.SecurityContext.RunAsUser == nil || *sc.SecurityContext.RunAsUser != 65532 {
		t.Errorf("sidecar RunAsUser not set to 65532")
	}
	// Restricted Pod Security rejects the whole pod without these.
	if ape := sc.SecurityContext.AllowPrivilegeEscalation; ape == nil || *ape {
		t.Errorf("sidecar AllowPrivilegeEscalation = %v, want false", ape)
	}
	if c := sc.SecurityContext.Capabilities; c == nil || len(c.Drop) != 1 || c.Drop[0] != "ALL" {
		t.Errorf("sidecar Capabilities = %v, want drop [ALL]", c)
	}
	wantProbeCmd := []string{BinaryPath, "probe", "--socket=/var/run/elb-envoy-remote-infra-provider/server.sock"}
	for name, p := range map[string]*corev1.Probe{
		"startupProbe":   sc.StartupProbe,
		"readinessProbe": sc.ReadinessProbe,
		"livenessProbe":  sc.LivenessProbe,
	} {
		if p == nil || p.Exec == nil {
			t.Errorf("expected an exec %s on the sidecar", name)
			continue
		}
		if !reflect.DeepEqual(p.Exec.Command, wantProbeCmd) {
			t.Errorf("%s command = %v, want %v", name, p.Exec.Command, wantProbeCmd)
		}
	}
	if len(sc.Args) == 0 || sc.Args[0] != "--server-socket-path=/var/run/elb-envoy-remote-infra-provider/server.sock" {
		t.Errorf("sidecar args = %v, want --server-socket-path default first", sc.Args)
	}

	// Shared socket volume present.
	if findVolume(pod, SocketVolumeName) == nil {
		t.Errorf("expected volume %q", SocketVolumeName)
	}

	// Pod fsGroup set.
	if pod.Spec.SecurityContext == nil || pod.Spec.SecurityContext.FSGroup == nil || *pod.Spec.SecurityContext.FSGroup != 65532 {
		t.Errorf("expected pod fsGroup 65532")
	}

	// envoy-gateway container got the socket mount.
	eg := findContainer(pod, "envoy-gateway")
	if !hasMount(eg, SocketVolumeName) {
		t.Errorf("expected socket volumeMount on envoy-gateway container")
	}
}

func TestInject_Idempotent(t *testing.T) {
	injector := NewSidecarInjector(Config{Image: "example.com/elb-envoy-remote-infra-provider:v1"})
	pod := egPod()

	injector.Inject(pod)
	injector.Inject(pod)
	injector.Inject(pod)

	initCount := 0
	for _, c := range pod.Spec.InitContainers {
		if c.Name == SidecarContainerName {
			initCount++
		}
	}
	if initCount != 1 {
		t.Errorf("sidecar initContainer count = %d, want 1 after repeated injection", initCount)
	}

	volCount := 0
	for _, v := range pod.Spec.Volumes {
		if v.Name == SocketVolumeName {
			volCount++
		}
	}
	if volCount != 1 {
		t.Errorf("socket volume count = %d, want 1", volCount)
	}

	eg := findContainer(pod, "envoy-gateway")
	mountCount := 0
	for _, vm := range eg.VolumeMounts {
		if vm.Name == SocketVolumeName {
			mountCount++
		}
	}
	if mountCount != 1 {
		t.Errorf("socket mount count on envoy-gateway = %d, want 1", mountCount)
	}
}

func TestInject_FindsEnvoyGatewayContainerByName(t *testing.T) {
	injector := NewSidecarInjector(Config{Image: "img"})
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "shim"},
				{Name: "envoy-gateway"},
				{Name: "other"},
			},
		},
	}

	injector.Inject(pod)

	if hasMount(findContainer(pod, "shim"), SocketVolumeName) {
		t.Errorf("did not expect socket mount on unrelated container 'shim'")
	}
	if hasMount(findContainer(pod, "other"), SocketVolumeName) {
		t.Errorf("did not expect socket mount on unrelated container 'other'")
	}
	if !hasMount(findContainer(pod, "envoy-gateway"), SocketVolumeName) {
		t.Errorf("expected socket mount only on envoy-gateway container")
	}
}

func TestInject_CustomEnvoyGatewayContainerName(t *testing.T) {
	injector := NewSidecarInjector(Config{Image: "img", EnvoyGatewayContainerName: "eg-custom"})
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "eg-custom"}},
		},
	}

	injector.Inject(pod)

	if !hasMount(findContainer(pod, "eg-custom"), SocketVolumeName) {
		t.Errorf("expected socket mount on configured container 'eg-custom'")
	}
}

// egPodWithCerts is an EG pod that already defines the certs volume (as EG's
// real control-plane pod does).
func egPodWithCerts() *corev1.Pod {
	pod := egPod()
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
		Name:         CertsVolumeName,
		VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "envoy-gateway"}},
	})
	return pod
}

func TestInject_MountCertsToggle(t *testing.T) {
	// MountCerts=false => sidecar has only the socket mount, even if certs volume exists.
	off := NewSidecarInjector(Config{Image: "img", MountCerts: false})
	pod := egPodWithCerts()
	off.Inject(pod)
	sc := findInitContainer(pod, SidecarContainerName)
	if hasMount(sc, CertsVolumeName) {
		t.Errorf("did not expect certs mount when MountCerts=false")
	}
	if !hasMount(sc, SocketVolumeName) {
		t.Errorf("expected socket mount on sidecar")
	}

	// MountCerts=true AND the pod has a certs volume => sidecar mounts certs.
	on := NewSidecarInjector(Config{Image: "img", MountCerts: true})
	pod2 := egPodWithCerts()
	on.Inject(pod2)
	sc2 := findInitContainer(pod2, SidecarContainerName)
	if !hasMount(sc2, CertsVolumeName) {
		t.Errorf("expected certs mount when MountCerts=true and certs volume exists")
	}
}

func TestInject_MountCertsSkippedWhenNoCertsVolume(t *testing.T) {
	// MountCerts=true but the pod has NO certs volume => must NOT add the mount
	// (referencing a non-existent volume makes the pod spec invalid).
	on := NewSidecarInjector(Config{Image: "img", MountCerts: true})
	pod := egPod() // no certs volume
	on.Inject(pod)
	sc := findInitContainer(pod, SidecarContainerName)
	if hasMount(sc, CertsVolumeName) {
		t.Errorf("must not add certs mount when the pod has no certs volume")
	}
}

func TestInject_SkipsNonEnvoyGatewayPods(t *testing.T) {
	// A pod without the envoy-gateway container (e.g. EG certgen Job, random pod)
	// must pass through completely untouched.
	on := NewSidecarInjector(Config{Image: "img"})
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "certgen"}},
		},
	}
	on.Inject(pod)

	if len(pod.Spec.InitContainers) != 0 {
		t.Errorf("expected no injection into a non-EG pod, got %d initContainers", len(pod.Spec.InitContainers))
	}
	if len(pod.Spec.Volumes) != 0 {
		t.Errorf("expected no volumes added to a non-EG pod")
	}
	if pod.Spec.SecurityContext != nil {
		t.Errorf("expected pod securityContext untouched on a non-EG pod")
	}
}

func TestInject_PreservesExistingFSGroup(t *testing.T) {
	injector := NewSidecarInjector(Config{Image: "img"})
	pod := egPod()
	pod.Spec.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptr.To(int64(1234))}

	injector.Inject(pod)

	if *pod.Spec.SecurityContext.FSGroup != 1234 {
		t.Errorf("fsGroup = %d, want existing 1234 preserved", *pod.Spec.SecurityContext.FSGroup)
	}
}

func TestInject_AppendsExtraArgs(t *testing.T) {
	injector := NewSidecarInjector(Config{Image: "img", ExtraArgs: []string{"--aws-vpc-id=vpc-123"}})
	pod := egPod()

	injector.Inject(pod)

	sc := findInitContainer(pod, SidecarContainerName)
	if len(sc.Args) != 2 || sc.Args[1] != "--aws-vpc-id=vpc-123" {
		t.Errorf("sidecar args = %v, want socket-path then --aws-vpc-id=vpc-123", sc.Args)
	}
}
