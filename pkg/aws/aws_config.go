// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/elb-envoy-remote-infra-provider/pkg/version"
	smithymiddleware "github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

const (
	userAgentPrefix = "elbproxy.k8s.aws"
)

// AWSConfigGenerator generates an aws.Config for the running environment.
type AWSConfigGenerator interface {
	GenerateAWSConfig(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error)
}

// NewAWSConfigGenerator constructs an AWSConfigGenerator.
func NewAWSConfigGenerator(cfg CloudConfig) AWSConfigGenerator {
	return &awsConfigGeneratorImpl{cfg: cfg}
}

type awsConfigGeneratorImpl struct {
	cfg CloudConfig
}

func (gen *awsConfigGeneratorImpl) GenerateAWSConfig(ctx context.Context, optFns ...func(*config.LoadOptions) error) (aws.Config, error) {
	// defaultOpts is the base AWS SDK configuration applied to every API client:
	// - WithRegion: AWS region from --aws-region or auto-detected via env vars.
	// - WithRetryMaxAttempts: configures max attempts from --aws-max-attempts on the
	//   SDK's default retryer, preserving any default wrapping (e.g. throttling) the
	//   SDK may add.
	// - WithAPIOptions: overrides the User-Agent header with a fixed
	//   "elbproxy.k8s.aws/<git-version>" string, stripping the SDK-generated
	//   metadata
	// TODO: add throttle middleware and metrics collector when implemented.
	defaultOpts := []func(*config.LoadOptions) error{
		config.WithRegion(gen.cfg.Region),
		config.WithRetryMaxAttempts(gen.cfg.MaxAttempts),
		config.WithAPIOptions([]func(*smithymiddleware.Stack) error{
			overrideUserAgentMiddleware(userAgentPrefix + "/" + version.GitVersion),
		}),
	}

	defaultOpts = append(defaultOpts, optFns...)
	awsConfig, err := config.LoadDefaultConfig(ctx, defaultOpts...)
	if err != nil {
		return aws.Config{}, err
	}

	return awsConfig, nil
}

// overrideUserAgentMiddleware returns a middleware that replaces the User-Agent
// header with the given value, stripping all SDK-generated metadata.
func overrideUserAgentMiddleware(ua string) func(stack *smithymiddleware.Stack) error {
	return func(stack *smithymiddleware.Stack) error {
		return stack.Build.Add(smithymiddleware.BuildMiddlewareFunc("OverrideUserAgent",
			func(ctx context.Context, in smithymiddleware.BuildInput, next smithymiddleware.BuildHandler) (smithymiddleware.BuildOutput, smithymiddleware.Metadata, error) {
				if req, ok := in.Request.(*smithyhttp.Request); ok {
					req.Header.Set("User-Agent", ua)
				}
				return next.HandleBuild(ctx, in)
			}), smithymiddleware.After)
	}
}

var _ AWSConfigGenerator = &awsConfigGeneratorImpl{}
