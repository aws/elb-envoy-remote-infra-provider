// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

// WaitForPodLog polls until the logs of a pod labelled app=app in namespace contain
// substr, calling poke before each check.
func (s *Suite) WaitForPodLog(t *testing.T, namespace, app, substr string, timeout time.Duration, poke func()) {
	t.Helper()
	cs, err := kubernetes.NewForConfig(s.RestConfig)
	if err != nil {
		t.Fatalf("create clientset: %v", err)
	}
	ctx, cancel := contextWithTimeout(t, timeout)
	defer cancel()
	beat := waitHeartbeat(t, fmt.Sprintf("logs of app=%s in %s to contain %q", app, namespace, substr), timeout)
	selector := labels.SelectorFromSet(labels.Set{"app": app}).String()
	err = wait.PollUntilContextCancel(ctx, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		poke()
		pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			beat("list pods: " + err.Error())
			return false, nil
		}
		for _, pod := range pods.Items {
			stream, err := cs.CoreV1().Pods(namespace).GetLogs(pod.Name, &corev1.PodLogOptions{}).Stream(ctx)
			if err != nil {
				beat("get logs: " + err.Error())
				continue
			}
			var buf bytes.Buffer
			_, err = io.Copy(&buf, stream)
			_ = stream.Close()
			if err == nil && strings.Contains(buf.String(), substr) {
				return true, nil
			}
		}
		beat(fmt.Sprintf("not in the logs of %d pod(s) yet", len(pods.Items)))
		return false, nil
	})
	if err != nil {
		t.Fatalf("logs of app=%s in %s never contained %q: %v", app, namespace, substr, err)
	}
	Logf(t, "logs of app=%s in %s contain %q", app, namespace, substr)
}
