// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
)

func TestPlatformSecretStoreRoundTripsInAdHocCommandLineContext(t *testing.T) {
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		t.Fatal(err)
	}
	service := "io.github.kukuyan.inputmethod.YunPin.test." + hex.EncodeToString(token)
	store, err := NewPlatformSecretStore(PlatformSecretStoreOptions{Service: service})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile := "roundtrip"
	value := []byte("bounded non-production keychain test value")
	t.Cleanup(func() { _ = store.Delete(context.Background(), profile) })

	if err := store.Save(ctx, profile, value); err != nil {
		t.Fatalf("save local macOS Keychain item: %v", err)
	}
	loaded, err := store.Load(ctx, profile)
	if err != nil {
		t.Fatalf("load local macOS Keychain item: %v", err)
	}
	defer zeroBytes(loaded)
	if !bytes.Equal(loaded, value) {
		t.Fatal("macOS Keychain round trip changed the value")
	}
	if err := store.Delete(ctx, profile); err != nil {
		t.Fatalf("delete local macOS Keychain item: %v", err)
	}
	if _, err := store.Load(ctx, profile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("deleted macOS Keychain item remained visible: %v", err)
	}
}
