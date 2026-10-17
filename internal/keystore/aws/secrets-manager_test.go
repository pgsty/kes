// Copyright 2026 PGSTY contributors.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/minio/kes/internal/keystore"
	kesdk "github.com/minio/kms-go/kes"
)

func isolateAWS(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE",
		"AWS_USE_FIPS_ENDPOINT", "AWS_USE_DUALSTACK_ENDPOINT",
		"AWS_ENDPOINT_URL", "AWS_ENDPOINT_URL_SECRETS_MANAGER",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "credentials"))
	t.Setenv("AWS_DEFAULTS_MODE", "standard")
	t.Setenv("AWS_RETRY_MODE", "standard")
}

func secretsServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if r.Header.Get("Authorization") != "" {
				t.Error("Status must remain an unsigned reachability probe")
			}
			w.WriteHeader(http.StatusForbidden) // An AWS endpoint can reject an unsigned GET.
			return
		}
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func testConfig(endpoint string) *Config {
	return &Config{
		Addr: endpoint, Region: "us-east-2", KMSKeyID: "alias/kes-test",
		Login: Credentials{AccessKey: "explicit", SecretKey: "test-secret", SessionToken: "test-token"},
	}
}

func connectStore(t *testing.T, cfg *Config) *Store {
	t.Helper()
	store, err := Connect(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSecretsManagerOperations(t *testing.T) {
	isolateAWS(t)
	var created, deleted atomic.Bool
	server := secretsServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=explicit/") || r.Header.Get("X-Amz-Security-Token") != "test-token" {
			t.Error("request did not use the configured signing credentials")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Header.Get("X-Amz-Target") {
		case "secretsmanager.CreateSecret":
			if body["Name"] != "key" || body["SecretString"] != "original" || body["KmsKeyId"] != "alias/kes-test" {
				t.Errorf("unexpected create request: %v", body)
			}
			if created.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"ResourceExistsException","Message":"exists"}`)
				return
			}
			created.Store(true)
			_, _ = io.WriteString(w, `{}`)
		case "secretsmanager.GetSecretValue":
			if body["SecretId"] == "binary" {
				_, _ = io.WriteString(w, `{"SecretBinary":"AP8B"}`)
			} else {
				_, _ = io.WriteString(w, `{"SecretString":"original"}`)
			}
		case "secretsmanager.DeleteSecret":
			if body["SecretId"] != "key" || body["ForceDeleteWithoutRecovery"] != true || body["RecoveryWindowInDays"] != nil {
				t.Errorf("delete semantics changed: %v", body)
			}
			deleted.Store(true)
			_, _ = io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected operation: %s", r.Header.Get("X-Amz-Target"))
			w.WriteHeader(http.StatusBadRequest)
		}
	})
	store := connectStore(t, testConfig(server.URL))
	ctx := context.Background()
	if err := store.Create(ctx, "key", []byte("original")); err != nil {
		t.Fatal(err)
	}
	if err := store.Set(ctx, "key", []byte("original")); !errors.Is(err, kesdk.ErrKeyExists) {
		t.Fatalf("Set must not overwrite an existing secret: %v", err)
	}
	for name, want := range map[string][]byte{"key": []byte("original"), "binary": {0, 255, 1}} {
		got, err := store.Get(ctx, name)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Get(%s): %v, %v", name, got, err)
		}
	}
	if err := store.Delete(ctx, "key"); err != nil || !deleted.Load() {
		t.Fatalf("Delete: %v", err)
	}
}

func TestSecretsManagerErrors(t *testing.T) {
	isolateAWS(t)
	for _, tc := range []struct {
		operation string
		code      string
		want      error
		message   string
	}{
		{"create", "ResourceExistsException", kesdk.ErrKeyExists, ""},
		{"get", "ResourceNotFoundException", kesdk.ErrKeyNotFound, ""},
		{"delete", "ResourceNotFoundException", kesdk.ErrKeyNotFound, ""},
		{"get", "DecryptionFailure", nil, "cannot access"},
		{"create", "AccessDeniedException", nil, "failed to create"},
		{"get", "AccessDeniedException", nil, "failed to read"},
		{"delete", "AccessDeniedException", nil, "failed to delete"},
	} {
		t.Run(tc.operation+"/"+tc.code, func(t *testing.T) {
			server := secretsServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"__type": tc.code, "Message": "test failure"})
			})
			store := connectStore(t, testConfig(server.URL))
			var err error
			switch tc.operation {
			case "create":
				err = store.Create(context.Background(), "key", []byte("value"))
			case "get":
				_, err = store.Get(context.Background(), "key")
			case "delete":
				err = store.Delete(context.Background(), "key")
			}
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestSecretsManagerPagination(t *testing.T) {
	isolateAWS(t)
	var fail atomic.Bool
	server := secretsServer(t, func(w http.ResponseWriter, r *http.Request) {
		var input struct{ NextToken string }
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		switch input.NextToken {
		case "":
			_, _ = io.WriteString(w, `{"SecretList":[{"Name":"z"},{"Name":"b"}],"NextToken":"second"}`)
		case "second":
			if fail.Load() {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"__type":"AccessDeniedException","Message":"denied"}`)
				return
			}
			_, _ = io.WriteString(w, `{"SecretList":[],"NextToken":"third"}`)
		case "third":
			_, _ = io.WriteString(w, `{"SecretList":[{"Name":"a"},{"Name":"prefix/one"},{"Name":"prefix/two"},{}]}`)
		default:
			t.Errorf("unexpected continuation token: %q", input.NextToken)
		}
	})
	store := connectStore(t, testConfig(server.URL))
	for _, tc := range []struct {
		prefix string
		n      int
		want   []string
		next   string
	}{
		{"", 2, []string{"a", "b"}, "prefix/one"},
		{"prefix/", 10, []string{"prefix/one", "prefix/two"}, ""},
		{"missing", 10, []string{}, ""},
	} {
		names, next, err := store.List(context.Background(), tc.prefix, tc.n)
		if err != nil || !reflect.DeepEqual(names, tc.want) || next != tc.next {
			t.Fatalf("List(%q, %d): %v, %q, %v", tc.prefix, tc.n, names, next, err)
		}
	}
	fail.Store(true)
	names, next, err := store.List(context.Background(), "", -1)
	if err == nil || names != nil || next != "" {
		t.Fatalf("failed pagination returned partial results: %v, %q, %v", names, next, err)
	}
}

func TestSecretsManagerCancellation(t *testing.T) {
	isolateAWS(t)
	server := secretsServer(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("canceled operation reached the server")
		w.WriteHeader(http.StatusBadRequest)
	})
	store := connectStore(t, testConfig(server.URL))
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if expired {
			cancel()
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			want = context.DeadlineExceeded
		}
		cancel()
		_, getErr := store.Get(ctx, "key")
		_, _, listErr := store.List(ctx, "", -1)
		for _, err := range []error{store.Create(ctx, "key", []byte("value")), getErr, store.Delete(ctx, "key"), listErr} {
			if !errors.Is(err, want) {
				t.Fatalf("context error was lost: %v, want %v", err, want)
			}
		}
		_, err := store.Status(ctx)
		if unreachable, ok := keystore.IsUnreachable(err); !ok || !errors.Is(unreachable.Err, want) {
			t.Fatalf("unexpected status error: %v", err)
		}
	}
}

func TestSecretsManagerCredentials(t *testing.T) {
	for _, mode := range []string{"explicit", "environment", "file", "partial", "container", "ec2"} {
		t.Run(mode, func(t *testing.T) {
			isolateAWS(t)
			server := secretsServer(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
			cfg := testConfig(server.URL)
			cfg.Login = Credentials{}
			want := mode
			switch mode {
			case "explicit", "environment", "partial":
				t.Setenv("AWS_ACCESS_KEY_ID", "environment")
				t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
				t.Setenv("AWS_SESSION_TOKEN", "token")
				if mode == "explicit" {
					cfg.Login = Credentials{AccessKey: "explicit", SecretKey: "secret", SessionToken: "token"}
				} else if mode == "partial" {
					cfg.Login = Credentials{SessionToken: "token"}
				}
			case "file":
				t.Setenv("AWS_PROFILE", "kes")
				files := map[string]string{
					"AWS_SHARED_CREDENTIALS_FILE": "[kes]\naws_access_key_id = file\naws_secret_access_key = secret\naws_session_token = token\n",
					"AWS_CONFIG_FILE":             "[malformed config must not be loaded\n",
				}
				for name, content := range files {
					if err := os.WriteFile(os.Getenv(name), []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			case "container", "ec2":
				metadata := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/latest/api/token" {
						w.Header().Set("X-Aws-Ec2-Metadata-Token-Ttl-Seconds", "21600")
						_, _ = io.WriteString(w, "imds-token")
						return
					}
					if mode == "ec2" && r.Header.Get("X-aws-ec2-metadata-token") != "imds-token" {
						t.Error("expected IMDSv2 token")
					}
					if r.URL.Path == "/latest/meta-data/iam/security-credentials/" {
						_, _ = io.WriteString(w, "kes-role")
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]string{
						"Code": "Success", "AccessKeyId": mode, "SecretAccessKey": "secret", "Token": "token",
						"Expiration": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
					})
				}))
				t.Cleanup(metadata.Close)
				if mode == "container" {
					t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", metadata.URL)
				} else {
					t.Setenv("AWS_EC2_METADATA_DISABLED", "false")
					t.Setenv("AWS_EC2_METADATA_SERVICE_ENDPOINT", metadata.URL)
				}
			}
			store := connectStore(t, cfg)
			got, err := store.client.Options().Credentials.Retrieve(context.Background())
			if mode == "partial" {
				var empty *credentials.StaticCredentialsEmptyError
				if !errors.As(err, &empty) {
					t.Fatalf("incomplete explicit credentials fell back: %v", err)
				}
			} else if err != nil || got.AccessKeyID != want || got.SecretAccessKey != "secret" || got.SessionToken != "token" {
				t.Fatalf("credential chain selected the wrong identity for %s: %v", mode, err)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSecretsManagerEndpoints(t *testing.T) {
	isolateAWS(t)
	// The SDK-specific endpoint variables must not redirect KES's configured store.
	t.Setenv("AWS_ENDPOINT_URL", "https://wrong.example")
	t.Setenv("AWS_ENDPOINT_URL_SECRETS_MANAGER", "https://wrong-service.example")
	oldClient := http.DefaultClient
	t.Cleanup(func() { http.DefaultClient = oldClient })
	var got string
	http.DefaultClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		got = r.URL.String()
		return &http.Response{StatusCode: http.StatusForbidden, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	for _, tc := range []struct{ endpoint, region, want string }{
		{"", "us-east-2", "https://secretsmanager.us-east-2.amazonaws.com"},
		{"", "cn-north-1", "https://secretsmanager.cn-north-1.amazonaws.com.cn"},
		{"", "us-gov-west-1", "https://secretsmanager.us-gov-west-1.amazonaws.com"},
		{"secretsmanager-fips.us-east-2.amazonaws.com", "us-east-2", "https://secretsmanager-fips.us-east-2.amazonaws.com"},
		{"http://localhost:5000", "us-east-2", "http://localhost:5000"},
		{"https://custom.example:8443/path", "us-east-2", "https://custom.example:8443/path"},
	} {
		cfg := testConfig(tc.endpoint)
		cfg.Region = tc.region
		store := connectStore(t, cfg)
		if got != tc.want || store.endpoint != tc.want {
			t.Fatalf("endpoint(%q, %q): %q, want %q", tc.endpoint, tc.region, got, tc.want)
		}
	}
	t.Setenv("AWS_REGION", "us-east-2")
	cfg := testConfig("")
	cfg.Region = ""
	if _, err := Connect(context.Background(), cfg); err == nil {
		t.Fatal("missing configured region was accepted")
	}
}
