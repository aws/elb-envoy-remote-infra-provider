// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package services

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgttypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/smithy-go/encoding/cbor"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRGTHTTPClient answers each GetResources request with the next canned page.
type fakeRGTHTTPClient struct {
	pages    [][]byte
	requests []cbor.Map
}

func (c *fakeRGTHTTPClient) Do(req *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	decoded, err := cbor.Decode(body)
	if err != nil {
		return nil, err
	}
	request, ok := decoded.(cbor.Map)
	if !ok {
		return nil, fmt.Errorf("request body is %T, want a CBOR map", decoded)
	}
	c.requests = append(c.requests, request)
	if len(c.requests) > len(c.pages) {
		return nil, fmt.Errorf("unexpected request %d", len(c.requests))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/cbor"}, "Smithy-Protocol": []string{"rpc-v2-cbor"}},
		Body:       io.NopCloser(bytes.NewReader(c.pages[len(c.requests)-1])),
		Request:    req,
	}, nil
}

// cborPage encodes a GetResources response page in Smithy RPC v2 CBOR, the protocol the
// tagging client uses.
func cborPage(nextToken string, arns ...string) []byte {
	var mappings cbor.List
	for _, arn := range arns {
		mappings = append(mappings, cbor.Map{
			"ResourceARN": cbor.String(arn),
			"Tags":        cbor.List{cbor.Map{"Key": cbor.String("k"), "Value": cbor.String("v")}},
		})
	}
	return cbor.Encode(cbor.Map{
		"PaginationToken":        cbor.String(nextToken),
		"ResourceTagMappingList": mappings,
	})
}

func newFakeRGTClient(httpClient *fakeRGTHTTPClient) *resourcegroupstaggingapi.Client {
	return resourcegroupstaggingapi.New(resourcegroupstaggingapi.Options{
		Region:           "us-east-1",
		Credentials:      aws.AnonymousCredentials{},
		HTTPClient:       httpClient,
		RetryMaxAttempts: 1,
	})
}

func TestNewRGT(t *testing.T) {
	client := NewRGT(&mockAWSClientsProvider{})
	assert.NotNil(t, client)
}

func TestGetResourcesAsList_ProviderError(t *testing.T) {
	providerErr := errors.New("provider error")
	client := NewRGT(&mockAWSClientsProvider{err: providerErr})

	_, err := client.GetResourcesAsList(context.Background(), &resourcegroupstaggingapi.GetResourcesInput{})
	assert.ErrorIs(t, err, providerErr)
}

func TestGetResourcesAsList_PassesCorrectOperation(t *testing.T) {
	provider := &operationCapturingProvider{rgtClient: resourcegroupstaggingapi.New(resourcegroupstaggingapi.Options{})}
	client := NewRGT(provider)

	_, _ = client.GetResourcesAsList(context.Background(), &resourcegroupstaggingapi.GetResourcesInput{})
	assert.Equal(t, rgtGetResources, provider.capturedOperation)
}

func TestGetResourcesAsList_FollowsPagination(t *testing.T) {
	httpClient := &fakeRGTHTTPClient{pages: [][]byte{
		cborPage("page-2", "arn:proxy-1"),
		cborPage("", "arn:proxy-2"),
	}}
	client := NewRGT(&mockAWSClientsProvider{rgtClient: newFakeRGTClient(httpClient)})

	got, err := client.GetResourcesAsList(context.Background(), &resourcegroupstaggingapi.GetResourcesInput{
		ResourceTypeFilters: []string{ResourceTypeELBProxy},
	})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "arn:proxy-1", aws.ToString(got[0].ResourceARN))
	assert.Equal(t, "arn:proxy-2", aws.ToString(got[1].ResourceARN))
	assert.Equal(t, map[string]string{"k": "v"}, ParseRGTTags(got[0].Tags))
	require.Len(t, httpClient.requests, 2)
	assert.Equal(t, cbor.List{cbor.String(ResourceTypeELBProxy)}, httpClient.requests[0]["ResourceTypeFilters"])
	assert.Nil(t, httpClient.requests[0]["PaginationToken"])
	assert.Equal(t, cbor.String("page-2"), httpClient.requests[1]["PaginationToken"])
}

func TestParseRGTTags(t *testing.T) {
	got := ParseRGTTags([]rgttypes.Tag{
		{Key: aws.String("keyA"), Value: aws.String("valueA")},
		{Key: aws.String("keyB"), Value: aws.String("")},
	})
	assert.Equal(t, map[string]string{"keyA": "valueA", "keyB": ""}, got)
}
