// SPDX-License-Identifier: Apache-2.0
package desktopagent

import (
	"context"
	"errors"
	"testing"
)

type memorySecretStore struct {
	values map[string][]byte
}

func newMemorySecretStore() *memorySecretStore {
	return &memorySecretStore{values: make(map[string][]byte)}
}

func (store *memorySecretStore) Load(ctx context.Context, profile string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, exists := store.values[profile]
	if !exists {
		return nil, ErrSecretNotFound
	}
	return append([]byte(nil), value...), nil
}

func (store *memorySecretStore) Save(ctx context.Context, profile string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.values[profile] = append([]byte(nil), value...)
	return nil
}

func (store *memorySecretStore) Delete(ctx context.Context, profile string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, exists := store.values[profile]; !exists {
		return ErrSecretNotFound
	}
	delete(store.values, profile)
	return nil
}

func TestInjectableSecretStoreCopiesValuesAndHonorsContext(t *testing.T) {
	store := newMemorySecretStore()
	value := []byte("synthetic-secret")
	if err := store.Save(context.Background(), "default", value); err != nil {
		t.Fatal(err)
	}
	value[0] ^= 0xff
	loaded, err := store.Load(context.Background(), "default")
	if err != nil || string(loaded) != "synthetic-secret" {
		t.Fatalf("load=%q err=%v", loaded, err)
	}
	loaded[0] ^= 0xff
	again, err := store.Load(context.Background(), "default")
	if err != nil || string(again) != "synthetic-secret" {
		t.Fatal("secret store exposed an internal mutable buffer")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(cancelled, "default"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load error=%v", err)
	}
}

func TestProfileValidationRejectsPathTraversal(t *testing.T) {
	for _, profile := range []string{"", "../default", "a/b", "a\\b", "space name"} {
		if err := validateProfile(profile); err == nil {
			t.Fatalf("unsafe profile %q was accepted", profile)
		}
	}
	for _, profile := range []string{"default", "work-1", "profile.name", "A_B"} {
		if err := validateProfile(profile); err != nil {
			t.Fatalf("safe profile %q: %v", profile, err)
		}
	}
}
