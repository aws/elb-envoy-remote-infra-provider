// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package framework

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
)

// HTTPRequest captures the inputs to a single probe attempt.
type HTTPRequest struct {
	Host   string // "host" or "host:port"; a bare host implies :80, or :443 with TLS
	Path   string // request path; empty means "/"
	Header map[string]string
	Method string // defaults to GET

	// ServerName turns on TLS and sets the SNI the server certificate must match.
	ServerName string
	// RootCAPEM is the PEM CA the server certificate must chain to.
	RootCAPEM []byte
}

// usesTLS reports whether the request is sent over HTTPS.
func (r HTTPRequest) usesTLS() bool { return r.ServerName != "" }

// protocol names the request's protocol in progress logs.
func (r HTTPRequest) protocol() string {
	if r.usesTLS() {
		return ProtoHTTPS
	}
	return ProtoHTTP
}

// newTransport returns a keep-alive-free transport for one attempt, and a release func.
func (r HTTPRequest) newTransport() (http.RoundTripper, func(), error) {
	var tlsConfig *tls.Config
	if r.usesTLS() {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(r.RootCAPEM) {
			return nil, nil, fmt.Errorf("RootCAPEM has no certificates for server name %q", r.ServerName)
		}
		tlsConfig = &tls.Config{ServerName: r.ServerName, RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	tr := &http.Transport{
		DisableKeepAlives: true,
		Proxy:             http.ProxyFromEnvironment,
		TLSClientConfig:   tlsConfig,
	}
	return tr, tr.CloseIdleConnections, nil
}

// HTTPExpectation describes the response that ends the probe.
type HTTPExpectation struct {
	StatusCode int
	// Header lists response headers that must be present with these values.
	Header map[string]string
}

// String describes the expected response, e.g. "status 200 with x-foo: bar".
func (e HTTPExpectation) String() string {
	desc := fmt.Sprintf("status %d", e.StatusCode)
	if len(e.Header) == 0 {
		return desc
	}
	names := slices.Sorted(maps.Keys(e.Header))
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+": "+e.Header[name])
	}
	return desc + " with " + strings.Join(pairs, ", ")
}

// headerMismatches describes each expected header that got lacks or has another value.
func headerMismatches(got http.Header, want map[string]string) []string {
	var mismatches []string
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if v := got.Get(name); v != want[name] {
			mismatches = append(mismatches, fmt.Sprintf("%s: %q (want %q)", name, v, want[name]))
		}
	}
	return mismatches
}

// MakeRequestAndExpectEventuallyConsistentResponse polls until the expected
// status code (and headers, if any) is seen. Retries absorb async xDS propagation
// and a cold load balancer (DNS/health-check warmup). Each attempt uses a fresh
// keep-alive-disabled client so a cached connection or DNS failure can't poison retries.
func (s *Suite) MakeRequestAndExpectEventuallyConsistentResponse(t *testing.T, req HTTPRequest, exp HTTPExpectation) {
	t.Helper()

	url := buildURL(req)
	Logf(t, "probing %s %s over %s (expect %s, up to %s)", methodOrDefault(req.Method), url, req.protocol(), exp, s.Timeouts.HTTPProbe)

	ctx, cancel := contextWithTimeout(t, s.Timeouts.HTTPProbe)
	defer cancel()

	var lastErr error
	var lastStatus int
	attempts := 0
	start := time.Now()

	err := wait.PollUntilContextTimeout(ctx, s.Timeouts.PollInterval, s.Timeouts.HTTPProbe, true, func(ctx context.Context) (bool, error) {
		attempts++

		// Fresh keep-alive-disabled client per attempt: forces a new connection and
		// DNS lookup while a new ELB is still coming up.
		transport, release, err := req.newTransport()
		if err != nil {
			return false, err // non-retryable: malformed input
		}
		defer release()
		httpClient := &http.Client{
			Timeout:       15 * time.Second,
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}

		hreq, err := http.NewRequestWithContext(ctx, methodOrDefault(req.Method), url, nil)
		if err != nil {
			return false, err // non-retryable: malformed input
		}
		for k, v := range req.Header {
			hreq.Header.Set(k, v)
		}
		// Allow Host override via header — net/http promotes it into req.Host.
		if h, ok := req.Header["Host"]; ok {
			hreq.Host = h
		}

		resp, err := httpClient.Do(hreq)
		if err != nil {
			lastErr = err
			logProgress(t, req.protocol(), attempts, start, classifyErr(err))
			return false, nil
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode
		lastErr = nil
		if resp.StatusCode != exp.StatusCode {
			logProgress(t, req.protocol(), attempts, start, fmt.Sprintf("status %d (want %d)", resp.StatusCode, exp.StatusCode))
			return false, nil
		}
		if mismatches := headerMismatches(resp.Header, exp.Header); len(mismatches) > 0 {
			lastErr = fmt.Errorf("status %d but headers %s", resp.StatusCode, strings.Join(mismatches, ", "))
			logProgress(t, req.protocol(), attempts, start, lastErr.Error())
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("expected %s from %s after %d attempts over %s; last status=%d, last error=%v",
			exp, url, attempts, time.Since(start).Round(time.Second), lastStatus, lastErr)
	}
	Logf(t, "got %s from %s after %d attempts (%s)", exp, url, attempts, time.Since(start).Round(time.Second))
}

// buildURL builds an http or https URL, defaulting to port 80 (443 with TLS) and path "/".
func buildURL(req HTTPRequest) string {
	scheme, port := "http", ":80"
	if req.usesTLS() {
		scheme, port = "https", ":443"
	}
	host := req.Host
	if !strings.Contains(host, ":") {
		host += port
	}
	path := req.Path
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return fmt.Sprintf("%s://%s%s", scheme, host, path)
}

func methodOrDefault(m string) string {
	if m == "" {
		return http.MethodGet
	}
	return m
}
