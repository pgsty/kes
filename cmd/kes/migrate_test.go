// Copyright 2026 - PGSTY contributors.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/minio/kms-go/kms"
	"github.com/minio/kms-go/kms/cmds"
)

func TestMigrateToKMS(t *testing.T) {
	// Run the command in a subprocess because CLI errors call os.Exit.
	if os.Getenv("KES_TEST_MIGRATE") == "1" {
		migrate(os.Args[slices.Index(os.Args, "--")+1:])
		return
	}

	// Encode the API key in the k1 format accepted by kms-go v0.5.0.
	seed := bytes.Repeat([]byte{1}, ed25519.SeedSize)
	apiKey := "k1:" + base64.RawURLEncoding.EncodeToString(seed)
	publicKey := ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	keyBytes := bytes.Repeat([]byte{2}, 32)
	keyName := "migration-key"

	dir := t.TempDir()
	keyDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storedKey, err := json.Marshal(struct {
		Bytes []byte `json:"bytes"`
	}{Bytes: keyBytes})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, keyName), storedKey, 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "kes.yml")
	config := fmt.Sprintf("admin:\n  identity: %s\ntls:\n  key: unused.key\n  cert: unused.cert\nkeystore:\n  fs:\n    path: %q\n", strings.Repeat("a", 64), keyDir)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var imported atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerKey, ok := r.TLS.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
		if !ok || !bytes.Equal(peerKey, publicKey) {
			t.Error("migration changed the API key's TLS identity")
			http.Error(w, "wrong identity", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/kms/test-enclave" {
			t.Errorf("unexpected migration endpoint: %s %s", r.Method, r.URL.Path)
			http.Error(w, "wrong endpoint", http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			http.Error(w, "unreadable request", http.StatusBadRequest)
			return
		}
		var request kms.ImportKeyRequest
		remaining, err := cmds.Decode(body, cmds.KeyImport, &request)
		if err != nil || len(remaining) != 0 || request.Name != keyName || request.Type != kms.AES256 || !bytes.Equal(request.Key, keyBytes) {
			t.Errorf("incorrect key import: decode error=%v, remaining=%d, name=%q, type=%v", err, len(remaining), request.Name, request.Type)
			http.Error(w, "wrong key import", http.StatusBadRequest)
			return
		}
		imported.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAnyClientCert, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	defer server.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigrateToKMS$", "--", "migrate",
		"--from", configPath, "--server", server.URL, "--enclave", "test-enclave", "--api-key", apiKey, "--insecure")
	command.Env = append(os.Environ(), "KES_TEST_MIGRATE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("migration failed: %v\n%s", err, output)
	}
	if got := imported.Load(); got != 1 {
		t.Fatalf("expected one imported key, got %d", got)
	}
	if remaining, err := os.ReadFile(filepath.Join(keyDir, keyName)); err != nil || !bytes.Equal(remaining, storedKey) {
		t.Fatalf("migration changed the source key: %v", err)
	}
}
