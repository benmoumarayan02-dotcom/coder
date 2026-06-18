package provider

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"golang.org/x/xerrors"

	"github.com/coder/coder/v2/aibridge/config"
)

// defaultBedrockSessionName is the STS role session name used when the provider
// does not configure one. A stable value keeps AssumeRole calls identifiable in
// CloudTrail.
const defaultBedrockSessionName = "coder-aibridge"

// bedrockCredentialCache lazily builds and memoizes the AWS credentials
// provider for a Bedrock-backed provider.
//
// The resolved provider is wrapped in aws.NewCredentialsCache, which refreshes
// temporary credentials before expiry and deduplicates concurrent refreshes, so
// a steady stream of requests does not produce one STS AssumeRole call per
// request. The cache is built once on first use and reused across every request
// for the owning provider. A failed build is not memoized, so a transient
// startup failure (for example an IRSA token that is not yet present) can
// recover on a later request.
type bedrockCredentialCache struct {
	cfg config.AWSBedrock

	mu    sync.Mutex
	creds aws.CredentialsProvider
}

func newBedrockCredentialCache(cfg config.AWSBedrock) *bedrockCredentialCache {
	return &bedrockCredentialCache{cfg: cfg}
}

// get returns the cached credentials provider, building it on first use.
func (c *bedrockCredentialCache) get(ctx context.Context) (aws.CredentialsProvider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.creds != nil {
		return c.creds, nil
	}

	creds, err := buildBedrockCredentials(ctx, c.cfg)
	if err != nil {
		return nil, err
	}
	c.creds = creds
	return creds, nil
}

// buildBedrockCredentials resolves the base identity (static keys or the AWS SDK
// default credential chain, which covers IRSA, Pod Identity, instance profile,
// shared profile, and environment variables) and, when a target role ARN is
// configured, assumes that role via STS. The result is wrapped in a credentials
// cache so callers reuse and rotate temporary credentials rather than
// re-resolving them per request.
func buildBedrockCredentials(ctx context.Context, cfg config.AWSBedrock) (aws.CredentialsProvider, error) {
	if cfg.Region == "" && cfg.BaseURL == "" {
		return nil, xerrors.New("region or base url required")
	}

	var loadOpts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}

	// Use static credentials when explicitly provided, otherwise fall back to
	// the SDK default credential chain.
	switch {
	// Both set: use static credentials directly.
	case cfg.AccessKey != "" && cfg.AccessKeySecret != "":
		loadOpts = append(loadOpts, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				cfg.AccessKey,
				cfg.AccessKeySecret,
				"",
			),
		))
	// Only one set: misconfiguration.
	case cfg.AccessKey != "" || cfg.AccessKeySecret != "":
		return nil, xerrors.New("both access key and access key secret must be provided together")
	// Neither set: SDK default credential chain resolves the base identity.
	default:
	}

	base, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, xerrors.Errorf("failed to load AWS Bedrock config: %w", err)
	}

	// The base identity signs requests directly unless a target role is
	// configured, in which case it signs the AssumeRole call and the resulting
	// temporary credentials sign Bedrock requests.
	credsProvider := base.Credentials
	if cfg.RoleARN != "" {
		sessionName := cfg.SessionName
		if sessionName == "" {
			sessionName = defaultBedrockSessionName
		}
		credsProvider = stscreds.NewAssumeRoleProvider(sts.NewFromConfig(base), cfg.RoleARN, func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = sessionName
			if cfg.ExternalID != "" {
				o.ExternalID = aws.String(cfg.ExternalID)
			}
		})
	}

	cache := aws.NewCredentialsCache(credsProvider)

	// Fail fast: ensure credentials can be resolved (including the AssumeRole
	// call) before any request is signed.
	if _, err := cache.Retrieve(ctx); err != nil {
		return nil, xerrors.Errorf("no AWS credentials found: %w", err)
	}

	return cache, nil
}
