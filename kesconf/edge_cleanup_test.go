// Copyright 2026 PGSTY contributors.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package kesconf_test

import (
	"context"
	"errors"
	"testing"

	"github.com/minio/kes/kesconf"
	kesdk "github.com/minio/kms-go/kes"
)

func TestKeyStoreCleanupPreservesExistingKeys(t *testing.T) {
	config := kesconf.FSKeyStore{Path: t.TempDir()}
	ctx := context.Background()
	store, err := config.Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Create(ctx, "existing-key", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	t.Run("owned keys", func(t *testing.T) {
		tracked := trackTestKeys(t, store)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel() // Cleanup must still work after the operation context expires.
		if err := tracked.Create(ctx, "test-key", []byte("remove")); err != nil {
			t.Fatal(err)
		}
		if err := tracked.Create(ctx, "existing-key", []byte("replace")); !errors.Is(err, kesdk.ErrKeyExists) {
			t.Fatalf("creating an existing key: %v", err)
		}
	})
	if _, err := store.Get(ctx, "test-key"); !errors.Is(err, kesdk.ErrKeyNotFound) {
		t.Fatalf("test key was not removed: %v", err)
	}
	value, err := store.Get(ctx, "existing-key")
	if err != nil || string(value) != "keep" {
		t.Fatalf("existing key changed: %q, %v", value, err)
	}
	t.Run("Create", func(t *testing.T) { testCreate(ctx, store, t, "cleanup-create") })
	t.Run("Get", func(t *testing.T) { testGet(ctx, store, t, "cleanup-get") })
	names, _, err := store.List(ctx, "", -1)
	if err != nil || len(names) != 1 || names[0] != "existing-key" {
		t.Fatalf("unexpected keys after helper cleanup: %v, %v", names, err)
	}
}
