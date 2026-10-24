// Copyright 2026 PGSTY contributors.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package gcp

import (
	"bytes"
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	secretmanagerpb "cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	kesdk "github.com/minio/kms-go/kes"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
)

type secretManagerFixture struct {
	secretmanagerpb.UnimplementedSecretManagerServiceServer
	t *testing.T
}

func (s *secretManagerFixture) CreateSecret(_ context.Context, r *secretmanagerpb.CreateSecretRequest) (*secretmanagerpb.Secret, error) {
	if r.Parent != "projects/test" || r.Secret.GetReplication().GetAutomatic() == nil {
		s.t.Errorf("unexpected create request: %v", r)
	}
	if r.SecretId == "exists" {
		return nil, status.Error(codes.AlreadyExists, "exists")
	}
	return &secretmanagerpb.Secret{Name: "projects/test/secrets/" + r.SecretId}, nil
}

func (s *secretManagerFixture) AddSecretVersion(_ context.Context, r *secretmanagerpb.AddSecretVersionRequest) (*secretmanagerpb.SecretVersion, error) {
	if r.Parent != "projects/test/secrets/key" || !bytes.Equal(r.GetPayload().GetData(), []byte{0, 255, 1}) {
		s.t.Errorf("secret payload or name changed: %v", r)
	}
	return &secretmanagerpb.SecretVersion{Name: r.Parent + "/versions/1"}, nil
}

func (s *secretManagerFixture) AccessSecretVersion(_ context.Context, r *secretmanagerpb.AccessSecretVersionRequest) (*secretmanagerpb.AccessSecretVersionResponse, error) {
	if r.Name == "projects/test/secrets/missing/versions/1" {
		return nil, status.Error(codes.NotFound, "missing")
	}
	if r.Name == "projects/test/secrets/denied/versions/1" {
		return nil, status.Error(codes.PermissionDenied, "denied")
	}
	if r.Name != "projects/test/secrets/key/versions/1" {
		s.t.Errorf("KES must read version 1: %v", r)
	}
	return &secretmanagerpb.AccessSecretVersionResponse{Payload: &secretmanagerpb.SecretPayload{Data: []byte{0, 255, 1}}}, nil
}

func (s *secretManagerFixture) DeleteSecret(_ context.Context, r *secretmanagerpb.DeleteSecretRequest) (*emptypb.Empty, error) {
	if r.Name == "projects/test/secrets/missing" {
		return nil, status.Error(codes.NotFound, "missing")
	}
	if r.Name != "projects/test/secrets/key" {
		s.t.Errorf("delete must address the whole secret: %v", r)
	}
	return &emptypb.Empty{}, nil
}

func (s *secretManagerFixture) ListSecrets(_ context.Context, r *secretmanagerpb.ListSecretsRequest) (*secretmanagerpb.ListSecretsResponse, error) {
	if r.Parent != "projects/test" {
		s.t.Errorf("unexpected list parent: %v", r)
	}
	switch r.PageToken {
	case "":
		return &secretmanagerpb.ListSecretsResponse{Secrets: []*secretmanagerpb.Secret{{Name: "projects/test/secrets/a"}}, NextPageToken: "next"}, nil
	case "next":
		return &secretmanagerpb.ListSecretsResponse{Secrets: []*secretmanagerpb.Secret{{Name: "projects/test/secrets/b"}}}, nil
	default:
		return nil, status.Error(codes.InvalidArgument, "unexpected page token")
	}
}

func TestSecretManagerOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	secretmanagerpb.RegisterSecretManagerServiceServer(server, &secretManagerFixture{t: t})
	go server.Serve(listener)
	t.Cleanup(func() { server.Stop(); listener.Close() })
	conn, err := grpc.NewClient("passthrough:///local-secret-manager", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	client, err := secretmanager.NewClient(ctx, option.WithGRPCConn(conn), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	store := &Store{client: client, config: &Config{ProjectID: "test"}}
	if err := store.Create(ctx, "key", []byte{0, 255, 1}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, "exists", nil); !errors.Is(err, kesdk.ErrKeyExists) {
		t.Fatalf("duplicate: %v", err)
	}
	if value, err := store.Get(ctx, "key"); err != nil || !bytes.Equal(value, []byte{0, 255, 1}) {
		t.Fatalf("get: %x, %v", value, err)
	}
	if _, err := store.Get(ctx, "missing"); !errors.Is(err, kesdk.ErrKeyNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err := store.Get(ctx, "denied"); err == nil || !strings.Contains(err.Error(), "PermissionDenied") {
		t.Fatalf("permission: %v", err)
	}
	if names, next, err := store.List(ctx, "", -1); err != nil || next != "" || !reflect.DeepEqual(names, []string{"a", "b"}) {
		t.Fatalf("paginated list: %v, %q, %v", names, next, err)
	}
	if err := store.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, "missing"); !errors.Is(err, kesdk.ErrKeyNotFound) {
		t.Fatalf("missing delete: %v", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := store.Get(canceled, "key"); err == nil {
		t.Fatal("canceled request succeeded")
	}
}
