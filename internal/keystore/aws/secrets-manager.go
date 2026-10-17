// Copyright 2019 - MinIO, Inc. All rights reserved.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package aws

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
	"github.com/minio/kes"
	xhttp "github.com/minio/kes/internal/http"
	"github.com/minio/kes/internal/keystore"
	kesdk "github.com/minio/kms-go/kes"
)

// Credentials represents static AWS credentials:
// access key, secret key and a session token
type Credentials struct {
	AccessKey    string // The AWS access key
	SecretKey    string // The AWS secret key
	SessionToken string // The AWS session token
}

// Config is a structure containing configuration
// options for connecting to the AWS SecretsManager.
type Config struct {
	// Addr is the HTTP address of the AWS Secret
	// Manager. In general, the address has the
	// following form:
	//  secretsmanager.<region>.amazonaws.com
	Addr string

	// Region is the AWS region. Even though the Addr
	// endpoint contains that information already, this
	// field is mandatory.
	Region string

	// The KMSKeyID is the AWS-KMS key ID specifying the
	// AWS-KMS key that is used to encrypt (and decrypt) the
	// values stored at AWS Secrets Manager.
	KMSKeyID string

	// Login contains the AWS credentials (access/secret key).
	Login Credentials
}

// Connect establishes and returns a Conn to a AWS SecretManager
// using the given config.
func Connect(ctx context.Context, config *Config) (*Store, error) {
	if config.Region == "" {
		return nil, errors.New("aws: no region specified")
	}
	options := []func(*awsconfig.LoadOptions) error{
		awsconfig.WithRegion(config.Region),
		// Match v1's SharedConfigDisable: read the credentials file, but not
		// ~/.aws/config. KES configuration remains authoritative for endpoints.
		awsconfig.WithSharedConfigFiles([]string{}),
	}
	if config.Login.AccessKey != "" || config.Login.SecretKey != "" || config.Login.SessionToken != "" {
		// Even incomplete explicit credentials must not fall back to another
		// identity. Otherwise use the SDK's environment/file/IAM credential chain.
		options = append(options, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.Login.AccessKey, config.Login.SecretKey, config.Login.SessionToken,
		)))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	client := secretsmanager.NewFromConfig(cfg, func(o *secretsmanager.Options) {
		o.BaseEndpoint = nil // Do not inherit SDK endpoint environment overrides.
		if config.Addr != "" {
			endpoint := config.Addr
			if !strings.Contains(endpoint, "://") {
				endpoint = "https://" + endpoint
			}
			o.BaseEndpoint = aws.String(endpoint)
			// Explicit endpoints (including FIPS hostnames) took precedence
			// over automatic endpoint selection in v1 as well.
			o.EndpointOptions.UseFIPSEndpoint = aws.FIPSEndpointStateDisabled
			o.EndpointOptions.UseDualStackEndpoint = aws.DualStackEndpointStateDisabled
		}
	})
	opts := client.Options()
	endpoint, err := opts.EndpointResolverV2.ResolveEndpoint(ctx, secretsmanager.EndpointParameters{
		Region:       aws.String(opts.Region),
		Endpoint:     opts.BaseEndpoint,
		UseFIPS:      aws.Bool(opts.EndpointOptions.UseFIPSEndpoint == aws.FIPSEndpointStateEnabled),
		UseDualStack: aws.Bool(opts.EndpointOptions.UseDualStackEndpoint == aws.DualStackEndpointStateEnabled),
	})
	if err != nil {
		return nil, err
	}
	c := &Store{
		config:   *config,
		client:   client,
		endpoint: endpoint.URI.String(),
	}
	if _, err = c.Status(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// Store is an AWS SecretsManager secret store.
type Store struct {
	config   Config
	client   *secretsmanager.Client
	endpoint string
}

func (s *Store) String() string { return "AWS SecretsManager: " + s.config.Addr }

// Status returns the current state of the AWS SecretsManager instance.
// In particular, whether it is reachable and the network latency.
func (s *Store) Status(ctx context.Context) (kes.KeyStoreState, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.endpoint, nil)
	if err != nil {
		return kes.KeyStoreState{}, err
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return kes.KeyStoreState{}, &keystore.ErrUnreachable{Err: err}
	}
	defer xhttp.DrainBody(resp.Body)

	return kes.KeyStoreState{
		Latency: time.Since(start),
	}, nil
}

// Create stores the given key-value pair at the AWS SecretsManager
// if and only if it doesn't exists. If such an entry already exists
// it returns kes.ErrKeyExists.
//
// If the SecretsManager.KMSKeyID is set AWS will use this key ID to
// encrypt the values. Otherwise, AWS will use the default key ID for
// encrypting secrets at the AWS SecretsManager.
func (s *Store) Create(ctx context.Context, name string, value []byte) error {
	createOpt := secretsmanager.CreateSecretInput{
		Name:         aws.String(name),
		SecretString: aws.String(string(value)),
	}
	if s.config.KMSKeyID != "" {
		createOpt.KmsKeyId = aws.String(s.config.KMSKeyID)
	}
	if _, err := s.client.CreateSecret(ctx, &createOpt); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var exists *types.ResourceExistsException
		if errors.As(err, &exists) {
			return kesdk.ErrKeyExists
		}
		return fmt.Errorf("aws: failed to create '%s': %v", name, err)
	}
	return nil
}

// Set stores the given key-value pair at the AWS SecretsManager
// if and only if it doesn't exists. If such an entry already exists
// it returns kes.ErrKeyExists.
//
// If the SecretsManager.KMSKeyID is set AWS will use this key ID to
// encrypt the values. Otherwise, AWS will use the default key ID for
// encrypting secrets at the AWS SecretsManager.
func (s *Store) Set(ctx context.Context, name string, value []byte) error {
	return s.Create(ctx, name, value)
}

// Get returns the value associated with the given key.
// If no entry for key exists, it returns kes.ErrKeyNotFound.
func (s *Store) Get(ctx context.Context, name string) ([]byte, error) {
	response, err := s.client.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(name),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		var decryption *types.DecryptionFailure
		var missing *types.ResourceNotFoundException
		switch {
		case errors.As(err, &decryption):
			return nil, fmt.Errorf("aws: cannot access '%s': %v", name, err)
		case errors.As(err, &missing):
			return nil, kesdk.ErrKeyNotFound
		}
		return nil, fmt.Errorf("aws: failed to read '%s': %v", name, err)
	}

	// AWS has two different ways to store a secret. Either as
	// "SecretString" or as "SecretBinary". While they *seem* to
	// be equivalent from an API point of view, AWS console e.g.
	// only shows "SecretString" not "SecretBinary".
	// However, AWS demands and specifies that only one is present -
	// either "SecretString" or "SecretBinary" - we can check which
	// one is present and safely assume that the other one isn't.
	var value []byte
	if response.SecretString != nil {
		value = []byte(*response.SecretString)
	} else {
		value = response.SecretBinary
	}
	return value, nil
}

// Delete removes the key-value pair from the AWS SecretsManager, if
// it exists.
func (s *Store) Delete(ctx context.Context, name string) error {
	_, err := s.client.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
		SecretId:                   aws.String(name),
		ForceDeleteWithoutRecovery: aws.Bool(true),
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		var missing *types.ResourceNotFoundException
		if errors.As(err, &missing) {
			return kesdk.ErrKeyNotFound
		}
		return fmt.Errorf("aws: failed to delete '%s': %v", name, err)
	}
	return nil
}

// List returns a new Iterator over the names of
// all stored keys.
// List returns the first n key names, that start with the given
// prefix, and the next prefix from which the listing should
// continue.
//
// It returns all keys with the prefix if n < 0 and less than n
// names if n is greater than the number of keys with the prefix.
//
// An empty prefix matches any key name. At the end of the listing
// or when there are no (more) keys starting with the prefix, the
// returned prefix is empty.
func (s *Store) List(ctx context.Context, prefix string, n int) ([]string, string, error) {
	var names []string
	pages := secretsmanager.NewListSecretsPaginator(s.client, &secretsmanager.ListSecretsInput{})
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		if err != nil {
			return nil, "", err
		}
		for _, secret := range page.SecretList {
			if secret.Name != nil {
				names = append(names, *secret.Name)
			}
		}
	}
	return keystore.List(names, prefix, n)
}

// Close closes the Store.
func (s *Store) Close() error { return nil }
