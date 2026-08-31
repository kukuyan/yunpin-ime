// SPDX-License-Identifier: Apache-2.0
//go:build darwin

package desktopagent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingLegacyLoader struct {
	value  []byte
	err    error
	loads  atomic.Int32
	onLoad func()
}

func (loader *countingLegacyLoader) Load(ctx context.Context, _ string) ([]byte, error) {
	loader.loads.Add(1)
	if loader.onLoad != nil {
		loader.onLoad()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if loader.err != nil {
		return nil, loader.err
	}
	return append([]byte(nil), loader.value...), nil
}

func encodedTestCredential(t *testing.T) []byte {
	t.Helper()
	encoded, err := EncodeCredentialBundle(testCredentials())
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func newTestPrivateFileStore(t *testing.T, directory string, legacy legacySecretLoader) *privateFileSecretStore {
	t.Helper()
	store, err := newPrivateFileSecretStore(directory, legacy)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func privateTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestPlatformSecretStoreRoundTripsInPrivateStateDirectory(t *testing.T) {
	directory := privateTempDir(t)
	store, err := NewPlatformSecretStore(PlatformSecretStoreOptions{
		Service:   "io.github.kukuyan.inputmethod.YunPin.test",
		Directory: directory,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	profile := "roundtrip"
	value := []byte("bounded non-production private-file test value")

	if err := store.Save(ctx, profile, value); err != nil {
		t.Fatalf("save private credential: %v", err)
	}
	loaded, err := store.Load(ctx, profile)
	if err != nil {
		t.Fatalf("load private credential: %v", err)
	}
	defer zeroBytes(loaded)
	if !bytes.Equal(loaded, value) {
		t.Fatal("private credential round trip changed the value")
	}
	backgroundStore, ok := store.(nonInteractiveSecretStore)
	if !ok {
		t.Fatal("macOS private store does not expose a non-interactive resident load")
	}
	backgroundLoaded, err := backgroundStore.LoadWithoutUserInteraction(ctx, profile)
	if err != nil {
		t.Fatalf("load private credential without UI: %v", err)
	}
	defer zeroBytes(backgroundLoaded)
	if !bytes.Equal(backgroundLoaded, value) {
		t.Fatal("non-interactive private credential load changed the value")
	}
	path := filepath.Join(directory, "credentials-roundtrip"+privateCredentialFileSuffix)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !privateFilePermissionsOK(path, info) {
		t.Fatalf("private credential path is unsafe: info=%v err=%v", info, err)
	}
	if err := store.Delete(ctx, profile); err != nil {
		t.Fatalf("delete private credential: %v", err)
	}
	if _, err := backgroundStore.LoadWithoutUserInteraction(ctx, profile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("deleted private credential remained visible: %v", err)
	}
}

func TestPlatformSecretStoreRequiresAbsoluteDirectory(t *testing.T) {
	if _, err := NewPlatformSecretStore(PlatformSecretStoreOptions{
		Service: "io.github.kukuyan.inputmethod.YunPin.test", Directory: "relative",
	}); err == nil {
		t.Fatal("relative private credential directory was accepted")
	}
}

func TestInteractiveLoadMigratesValidatedDefaultCredentialOnce(t *testing.T) {
	directory := privateTempDir(t)
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, directory, legacy)

	loaded, err := store.Load(context.Background(), DefaultProfile)
	if err != nil {
		t.Fatalf("migrate default credential: %v", err)
	}
	defer zeroBytes(loaded)
	if !bytes.Equal(loaded, legacy.value) || legacy.loads.Load() != 1 {
		t.Fatalf("migration result differs or legacy load count=%d", legacy.loads.Load())
	}
	path, err := store.path(DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !privateFilePermissionsOK(path, info) {
		t.Fatalf("migrated credential is not private: info=%v err=%v", info, err)
	}

	legacy.err = errors.New("legacy store must not be read again")
	again, err := store.Load(context.Background(), DefaultProfile)
	if err != nil {
		t.Fatalf("load already migrated credential: %v", err)
	}
	defer zeroBytes(again)
	if !bytes.Equal(again, loaded) || legacy.loads.Load() != 1 {
		t.Fatalf("existing private credential did not win; legacy loads=%d", legacy.loads.Load())
	}
}

func TestConcurrentInteractiveLoadsReadLegacyOnce(t *testing.T) {
	directory := privateTempDir(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	legacy.onLoad = func() {
		startOnce.Do(func() {
			close(started)
			<-release
		})
	}
	store := newTestPrivateFileStore(t, directory, legacy)

	var values [2][]byte
	var errs [2]error
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		values[0], errs[0] = store.Load(context.Background(), DefaultProfile)
	}()
	<-started
	go func() {
		defer wait.Done()
		values[1], errs[1] = store.Load(context.Background(), DefaultProfile)
	}()
	time.Sleep(30 * time.Millisecond)
	if legacy.loads.Load() != 1 {
		t.Fatalf("concurrent load reached legacy %d times while migration was locked", legacy.loads.Load())
	}
	close(release)
	wait.Wait()
	defer zeroBytes(values[0])
	defer zeroBytes(values[1])
	if errs[0] != nil || errs[1] != nil || !bytes.Equal(values[0], values[1]) || legacy.loads.Load() != 1 {
		t.Fatalf("concurrent migration values_equal=%t errors=%v legacy_loads=%d", bytes.Equal(values[0], values[1]), errs, legacy.loads.Load())
	}
}

func TestConcurrentSaveWaitsForMigrationAndWins(t *testing.T) {
	directory := privateTempDir(t)
	started := make(chan struct{})
	release := make(chan struct{})
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	legacy.onLoad = func() {
		close(started)
		<-release
	}
	store := newTestPrivateFileStore(t, directory, legacy)

	loadDone := make(chan error, 1)
	go func() {
		value, err := store.Load(context.Background(), DefaultProfile)
		zeroBytes(value)
		loadDone <- err
	}()
	<-started
	fresh := []byte("fresh private credential saved after migration")
	saveDone := make(chan error, 1)
	go func() { saveDone <- store.Save(context.Background(), DefaultProfile, fresh) }()
	select {
	case err := <-saveDone:
		t.Fatalf("concurrent save bypassed migration lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-loadDone; err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	if err := <-saveDone; err != nil {
		t.Fatalf("fresh save failed: %v", err)
	}
	loaded, err := store.Load(context.Background(), DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(loaded)
	if !bytes.Equal(loaded, fresh) || legacy.loads.Load() != 1 {
		t.Fatalf("fresh save was overwritten or legacy loads=%d", legacy.loads.Load())
	}
}

func TestDeleteDefaultBlocksLegacyResurrection(t *testing.T) {
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, privateTempDir(t), legacy)
	loaded, err := store.Load(context.Background(), DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	zeroBytes(loaded)
	if err := store.Delete(context.Background(), DefaultProfile); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), DefaultProfile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("deleted default credential was resurrected: %v", err)
	}
	if legacy.loads.Load() != 1 {
		t.Fatalf("delete allowed a second legacy load: %d", legacy.loads.Load())
	}
}

func TestCredentialLockWaitHonorsContext(t *testing.T) {
	store := newTestPrivateFileStore(t, privateTempDir(t), nil)
	lock, err := store.acquireLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := store.Save(ctx, "blocked", []byte("value")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("credential lock wait ignored context: %v", err)
	}
}

func TestInteractiveLoadDoesNotMigrateUserSession(t *testing.T) {
	directory := privateTempDir(t)
	legacy := &countingLegacyLoader{value: []byte("bounded opaque user session")}
	store := newTestPrivateFileStore(t, directory, legacy)

	if _, err := store.Load(context.Background(), DefaultProfile+userSessionProfileSuffix); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("user session legacy fallback error=%v", err)
	}
	if legacy.loads.Load() != 0 {
		t.Fatalf("user session read legacy store %d times", legacy.loads.Load())
	}
}

func TestUnsafeDefaultTombstonePathFailsClosedWithoutLegacyRead(t *testing.T) {
	directory := privateTempDir(t)
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, directory, legacy)
	path, err := store.path(DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(directory, "tombstone-target")
	if err := os.WriteFile(target, []byte(privateCredentialTombstone), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), DefaultProfile); err == nil || errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("unsafe tombstone path did not fail closed: %v", err)
	}
	if legacy.loads.Load() != 0 {
		t.Fatalf("unsafe tombstone path reached legacy %d times", legacy.loads.Load())
	}
}

func TestDeleteMissingDefaultCommitsTombstoneAndBlocksLegacy(t *testing.T) {
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, privateTempDir(t), legacy)
	if err := store.Delete(context.Background(), DefaultProfile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("delete missing default error=%v", err)
	}
	if _, err := store.Load(context.Background(), DefaultProfile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("default tombstone did not block legacy: %v", err)
	}
	if legacy.loads.Load() != 0 {
		t.Fatalf("default tombstone reached legacy %d times", legacy.loads.Load())
	}
	path, err := store.path(DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := readBoundedRegular(path, int64(len(privateCredentialTombstone)))
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(contents)
	if !bytes.Equal(contents, []byte(privateCredentialTombstone)) {
		t.Fatal("delete did not persist the canonical default tombstone")
	}
}

func TestSaveReactivatesDefaultAfterTombstone(t *testing.T) {
	store := newTestPrivateFileStore(t, privateTempDir(t), nil)
	if err := store.Delete(context.Background(), DefaultProfile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("delete missing default error=%v", err)
	}
	value := encodedTestCredential(t)
	defer zeroBytes(value)
	if err := store.Save(context.Background(), DefaultProfile, value); err != nil {
		t.Fatalf("save default over tombstone: %v", err)
	}
	loaded, err := store.LoadWithoutUserInteraction(context.Background(), DefaultProfile)
	if err != nil {
		t.Fatalf("load reactivated default: %v", err)
	}
	defer zeroBytes(loaded)
	if !bytes.Equal(loaded, value) {
		t.Fatal("save over tombstone changed the credential")
	}
}

func TestNonInteractiveLoadDoesNotWaitForInteractiveMigration(t *testing.T) {
	directory := privateTempDir(t)
	started := make(chan struct{})
	release := make(chan struct{})
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	legacy.onLoad = func() {
		close(started)
		<-release
	}
	store := newTestPrivateFileStore(t, directory, legacy)
	loadDone := make(chan error, 1)
	go func() {
		value, err := store.Load(context.Background(), DefaultProfile)
		zeroBytes(value)
		loadDone <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := store.LoadWithoutUserInteraction(ctx, DefaultProfile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("non-interactive load during migration error=%v", err)
	}
	close(release)
	if err := <-loadDone; err != nil {
		t.Fatalf("interactive migration failed after release: %v", err)
	}
	if legacy.loads.Load() != 1 {
		t.Fatalf("non-interactive load touched legacy: %d", legacy.loads.Load())
	}
}

func TestNonInteractiveLoadNeverFallsBackToLegacy(t *testing.T) {
	directory := privateTempDir(t)
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, directory, legacy)

	if _, err := store.LoadWithoutUserInteraction(context.Background(), DefaultProfile); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("non-interactive missing load error=%v", err)
	}
	if legacy.loads.Load() != 0 {
		t.Fatalf("non-interactive load touched legacy store %d times", legacy.loads.Load())
	}
	path, _ := store.path(DefaultProfile)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("non-interactive load created a credential file: %v", err)
	}
}

func TestExistingPrivateFileNeverReadsLegacy(t *testing.T) {
	directory := privateTempDir(t)
	value := encodedTestCredential(t)
	writer := newTestPrivateFileStore(t, directory, nil)
	if err := writer.Save(context.Background(), DefaultProfile, value); err != nil {
		t.Fatal(err)
	}
	legacy := &countingLegacyLoader{err: errors.New("unexpected legacy access")}
	reader := newTestPrivateFileStore(t, directory, legacy)
	loaded, err := reader.Load(context.Background(), DefaultProfile)
	if err != nil {
		t.Fatal(err)
	}
	defer zeroBytes(loaded)
	if !bytes.Equal(loaded, value) || legacy.loads.Load() != 0 {
		t.Fatalf("existing file did not bypass legacy; loads=%d", legacy.loads.Load())
	}
}

func TestLegacyMigrationRejectsInvalidBundleWithoutArtifact(t *testing.T) {
	directory := privateTempDir(t)
	legacy := &countingLegacyLoader{value: []byte("not a credential bundle")}
	store := newTestPrivateFileStore(t, directory, legacy)

	if _, err := store.Load(context.Background(), DefaultProfile); err == nil {
		t.Fatal("invalid legacy bundle was migrated")
	}
	if legacy.loads.Load() != 1 {
		t.Fatalf("legacy load count=%d", legacy.loads.Load())
	}
	path, _ := store.path(DefaultProfile)
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed migration left a destination artifact: %v", err)
	}
}

func TestLegacyMigrationWriteFailureLeavesNoTemporaryArtifact(t *testing.T) {
	directory := privateTempDir(t)
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, directory, legacy)
	legacy.onLoad = func() {
		if err := os.Chmod(directory, 0755); err != nil {
			t.Fatalf("make state directory unsafe: %v", err)
		}
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0700) })

	if _, err := store.Load(context.Background(), DefaultProfile); err == nil {
		t.Fatal("migration unexpectedly wrote through an unsafe directory")
	}
	if legacy.loads.Load() != 1 {
		t.Fatalf("legacy load count=%d", legacy.loads.Load())
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != privateCredentialLockName {
			t.Fatalf("failed migration left an unexpected artifact: %v", entries)
		}
	}
}

func TestUnsafePrimaryObjectsDoNotFallBackToLegacy(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, directory, path string)
	}{
		{
			name: "world-readable file",
			setup: func(t *testing.T, _ string, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("unsafe"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink file",
			setup: func(t *testing.T, directory, path string) {
				t.Helper()
				target := filepath.Join(directory, "target")
				if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := privateTempDir(t)
			legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
			store := newTestPrivateFileStore(t, directory, legacy)
			path, _ := store.path(DefaultProfile)
			test.setup(t, directory, path)
			if _, err := store.Load(context.Background(), DefaultProfile); err == nil {
				t.Fatal("unsafe primary object was accepted")
			}
			if legacy.loads.Load() != 0 {
				t.Fatalf("unsafe primary object caused %d legacy loads", legacy.loads.Load())
			}
		})
	}
}

func TestUnsafeDirectoryDoesNotFallBackToLegacy(t *testing.T) {
	parent := t.TempDir()
	realDirectory := filepath.Join(parent, "real")
	if err := os.Mkdir(realDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "state-link")
	if err := os.Symlink(realDirectory, link); err != nil {
		t.Fatal(err)
	}
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, link, legacy)
	if _, err := store.Load(context.Background(), DefaultProfile); err == nil {
		t.Fatal("symlink credential directory was accepted")
	}
	if legacy.loads.Load() != 0 {
		t.Fatalf("unsafe directory caused %d legacy loads", legacy.loads.Load())
	}
}

func TestLegacyMigrationIsRestrictedToDefaultProfiles(t *testing.T) {
	legacy := &countingLegacyLoader{value: encodedTestCredential(t)}
	store := newTestPrivateFileStore(t, privateTempDir(t), legacy)
	if _, err := store.Load(context.Background(), "custom"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("custom legacy profile error=%v", err)
	}
	if legacy.loads.Load() != 0 {
		t.Fatalf("custom profile read legacy store %d times", legacy.loads.Load())
	}
}

func TestSaveAndDeleteNeverReadLegacyStore(t *testing.T) {
	legacyValue := encodedTestCredential(t)
	legacy := &countingLegacyLoader{value: append([]byte(nil), legacyValue...)}
	store := newTestPrivateFileStore(t, privateTempDir(t), legacy)
	value := []byte("fresh private credential")
	if err := store.Save(context.Background(), "fresh", value); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(context.Background(), "fresh"); err != nil {
		t.Fatal(err)
	}
	if legacy.loads.Load() != 0 || !bytes.Equal(legacy.value, legacyValue) {
		t.Fatal("fresh save or delete touched legacy storage")
	}
}
