package feishu

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	pkgcrypto "numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"
)

type task2FakeVaultAccountStore struct {
	accounts map[uint]*model.UserThirdPartyAccount
	getCalls int
}

func (s *task2FakeVaultAccountStore) Get(_ context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error) {
	s.getCalls++
	account, ok := s.accounts[userID]
	if !ok || provider != ProviderLark {
		return nil, gorm.ErrRecordNotFound
	}
	clone := *account
	return &clone, nil
}

type task2FakeCLIHomeVaultStore struct {
	vaults      map[uint]*model.FeishuCLIVault
	getCalls    int
	putCalls    int
	putExpected []uint64
	beforePut   func()
	putErr      error
}

func (s *task2FakeCLIHomeVaultStore) GetVault(_ context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error) {
	s.getCalls++
	vault, ok := s.vaults[userID]
	if !ok || vault.Generation != generation {
		return nil, gorm.ErrRecordNotFound
	}
	return cloneTask2Vault(vault), nil
}

func (s *task2FakeCLIHomeVaultStore) PutVaultCAS(_ context.Context, candidate *model.FeishuCLIVault, expectedRevision uint64) error {
	s.putCalls++
	s.putExpected = append(s.putExpected, expectedRevision)
	if s.beforePut != nil {
		s.beforePut()
	}
	if s.putErr != nil {
		return s.putErr
	}
	existing, exists := s.vaults[candidate.UserID]
	if expectedRevision == 0 {
		if exists {
			return gorm.ErrRecordNotFound
		}
	} else if !exists || existing.Generation != candidate.Generation || existing.Revision != expectedRevision {
		return gorm.ErrRecordNotFound
	}
	stored := cloneTask2Vault(candidate)
	stored.Revision = expectedRevision + 1
	s.vaults[candidate.UserID] = stored
	candidate.Revision = stored.Revision
	return nil
}

func cloneTask2Vault(vault *model.FeishuCLIVault) *model.FeishuCLIVault {
	if vault == nil {
		return nil
	}
	clone := *vault
	clone.Ciphertext = append([]byte(nil), vault.Ciphertext...)
	return &clone
}

type task2VaultFixture struct {
	vault       *EncryptedCLIHomeVault
	cipher      *pkgcrypto.Cipher
	accounts    *task2FakeVaultAccountStore
	store       *task2FakeCLIHomeVaultStore
	runtimeBase string
	userID      uint
	generation  uint64
	keyVersion  string
}

func task2NewCipher(t *testing.T) *pkgcrypto.Cipher {
	t.Helper()
	rawKey := make([]byte, pkgcrypto.KeyLen)
	_, err := rand.Read(rawKey)
	require.NoError(t, err)
	cipher, err := pkgcrypto.NewCipher(base64.StdEncoding.EncodeToString(rawKey))
	require.NoError(t, err)
	return cipher
}

func newTask2VaultFixture(t *testing.T, userID uint, generation uint64) *task2VaultFixture {
	t.Helper()

	cipher := task2NewCipher(t)

	runtimeBase := filepath.Join(t.TempDir(), "runtime-homes")
	require.NoError(t, os.Mkdir(runtimeBase, 0o755))
	accounts := &task2FakeVaultAccountStore{accounts: map[uint]*model.UserThirdPartyAccount{
		userID: {UserID: userID, Provider: ProviderLark, Generation: generation},
	}}
	store := &task2FakeCLIHomeVaultStore{vaults: make(map[uint]*model.FeishuCLIVault)}
	vault, err := NewEncryptedCLIHomeVault(accounts, store, cipher, "v1", runtimeBase)
	require.NoError(t, err)
	return &task2VaultFixture{
		vault:       vault,
		cipher:      cipher,
		accounts:    accounts,
		store:       store,
		runtimeBase: runtimeBase,
		userID:      userID,
		generation:  generation,
		keyVersion:  "v1",
	}
}

func newTask2KeyringVaultFixture(
	t *testing.T,
	userID uint,
	generation uint64,
	ciphers map[string]*pkgcrypto.Cipher,
	currentKeyVersion string,
) *task2VaultFixture {
	t.Helper()
	runtimeBase := filepath.Join(t.TempDir(), "runtime-homes")
	require.NoError(t, os.Mkdir(runtimeBase, 0o700))
	accounts := &task2FakeVaultAccountStore{accounts: map[uint]*model.UserThirdPartyAccount{
		userID: {UserID: userID, Provider: ProviderLark, Generation: generation},
	}}
	store := &task2FakeCLIHomeVaultStore{vaults: make(map[uint]*model.FeishuCLIVault)}
	vault, err := NewEncryptedCLIHomeVaultWithKeyring(
		accounts,
		store,
		ciphers,
		currentKeyVersion,
		runtimeBase,
	)
	require.NoError(t, err)
	return &task2VaultFixture{
		vault:       vault,
		cipher:      ciphers[currentKeyVersion],
		accounts:    accounts,
		store:       store,
		runtimeBase: runtimeBase,
		userID:      userID,
		generation:  generation,
		keyVersion:  currentKeyVersion,
	}
}

func (f *task2VaultFixture) putEncryptedArchive(t *testing.T, archive []byte, revision uint64) {
	t.Helper()
	task2PutEncryptedArchive(
		t,
		f.store,
		f.cipher,
		f.userID,
		f.generation,
		f.keyVersion,
		revision,
		archive,
	)
}

func task2PutEncryptedArchive(
	t *testing.T,
	store *task2FakeCLIHomeVaultStore,
	cipher *pkgcrypto.Cipher,
	userID uint,
	generation uint64,
	keyVersion string,
	revision uint64,
	archive []byte,
) {
	t.Helper()
	aad := []byte(fmt.Sprintf("lark|%d|%d|%s", userID, generation, keyVersion))
	ciphertext, err := cipher.EncryptWithAAD(archive, aad)
	require.NoError(t, err)
	store.vaults[userID] = &model.FeishuCLIVault{
		UserID:     userID,
		Generation: generation,
		Ciphertext: ciphertext,
		KeyVersion: keyVersion,
		Checksum:   task2CiphertextChecksum(ciphertext),
		Revision:   revision,
	}
}

func task2CiphertextChecksum(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	return hex.EncodeToString(sum[:])
}

type task2TarEntry struct {
	name     string
	typeflag byte
	mode     int64
	linkname string
	body     []byte
}

func task2TarArchive(t *testing.T, entries ...task2TarEntry) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	for _, entry := range entries {
		mode := entry.mode
		if mode == 0 {
			mode = 0o777
		}
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeflag,
			Mode:     mode,
			Linkname: entry.linkname,
		}
		if entry.typeflag == tar.TypeReg || entry.typeflag == 0 {
			header.Size = int64(len(entry.body))
		}
		require.NoError(t, writer.WriteHeader(header))
		if len(entry.body) > 0 {
			_, err := writer.Write(entry.body)
			require.NoError(t, err)
		}
	}
	require.NoError(t, writer.Close())
	return buffer.Bytes()
}

func task2RequireMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, want, info.Mode().Perm(), "unexpected mode for %s", path)
}

func task2RequireNoRuntimeHomes(t *testing.T, base string) {
	t.Helper()
	entries, err := os.ReadDir(base)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestEncryptedCLIHomeVault_KeyRotationReadsOldVersionWithoutResealing(t *testing.T) {
	v1 := task2NewCipher(t)
	v2 := task2NewCipher(t)
	inputKeyring := map[string]*pkgcrypto.Cipher{"v1": v1, "v2": v2}
	f := newTask2KeyringVaultFixture(t, 7, 1, inputKeyring, "v2")
	task2PutEncryptedArchive(
		t,
		f.store,
		v1,
		f.userID,
		f.generation,
		"v1",
		1,
		task2TarArchive(t, task2TarEntry{
			name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"key":"v1"}`),
		}),
	)
	original := cloneTask2Vault(f.store.vaults[f.userID])
	delete(inputKeyring, "v1") // The vault must use a frozen copy, not this caller-owned map.

	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		body, readErr := os.ReadFile(filepath.Join(home, "config.json"))
		require.NoError(t, readErr)
		require.JSONEq(t, `{"key":"v1"}`, string(body))
		return false, nil
	})
	require.NoError(t, err)
	require.Zero(t, f.store.putCalls)
	require.Equal(t, original, f.store.vaults[f.userID])
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_KeyRotationResealsChangedSnapshotWithCurrentKey(t *testing.T) {
	v1 := task2NewCipher(t)
	v2 := task2NewCipher(t)
	f := newTask2KeyringVaultFixture(
		t,
		7,
		1,
		map[string]*pkgcrypto.Cipher{"v1": v1, "v2": v2},
		"v2",
	)
	task2PutEncryptedArchive(
		t,
		f.store,
		v1,
		f.userID,
		f.generation,
		"v1",
		1,
		task2TarArchive(t, task2TarEntry{
			name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"key":"v1"}`),
		}),
	)

	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		return true, os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"key":"v2"}`), 0o600)
	})
	require.NoError(t, err)
	stored := f.store.vaults[f.userID]
	require.Equal(t, "v2", stored.KeyVersion)
	require.Equal(t, uint64(2), stored.Revision)
	require.Equal(t, task2CiphertextChecksum(stored.Ciphertext), stored.Checksum)
	_, err = v2.DecryptWithAAD(stored.Ciphertext, []byte("lark|7|1|v2"))
	require.NoError(t, err)
	_, err = v1.DecryptWithAAD(stored.Ciphertext, []byte("lark|7|1|v2"))
	require.Error(t, err)

	err = f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		body, readErr := os.ReadFile(filepath.Join(home, "config.json"))
		require.NoError(t, readErr)
		require.JSONEq(t, `{"key":"v2"}`, string(body))
		return false, nil
	})
	require.NoError(t, err)
	require.Equal(t, 1, f.store.putCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_KeyRotationRejectsMissingHistoricalKey(t *testing.T) {
	v1 := task2NewCipher(t)
	v2 := task2NewCipher(t)
	f := newTask2KeyringVaultFixture(
		t,
		7,
		1,
		map[string]*pkgcrypto.Cipher{"v2": v2},
		"v2",
	)
	task2PutEncryptedArchive(
		t,
		f.store,
		v1,
		f.userID,
		f.generation,
		"v1",
		1,
		task2TarArchive(t, task2TarEntry{
			name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"key":"v1"}`),
		}),
	)

	callbackCalled := false
	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(string) (bool, error) {
		callbackCalled = true
		return false, nil
	})
	require.Error(t, err)
	require.False(t, callbackCalled)
	require.Zero(t, f.store.putCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_KeyVersionValidation(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	validVersions := []string{"v", "v1.release-2_key", strings.Repeat("A", 32)}
	for _, version := range validVersions {
		t.Run("valid_"+version, func(t *testing.T) {
			_, err := NewEncryptedCLIHomeVault(f.accounts, f.store, f.cipher, version, f.runtimeBase)
			require.NoError(t, err)
		})
	}

	invalidVersions := []string{
		"",
		strings.Repeat("A", 33),
		"版本1",
		"v1|next",
		"v1 next",
		"v1/next",
	}
	for _, version := range invalidVersions {
		t.Run("invalid_"+version, func(t *testing.T) {
			_, err := NewEncryptedCLIHomeVault(f.accounts, f.store, f.cipher, version, f.runtimeBase)
			require.Error(t, err)
		})
	}

	_, err := NewEncryptedCLIHomeVaultWithKeyring(
		f.accounts,
		f.store,
		map[string]*pkgcrypto.Cipher{"v2": f.cipher, "old/version": task2NewCipher(t)},
		"v2",
		f.runtimeBase,
	)
	require.Error(t, err, "every historical key version must be validated")

	_, err = NewEncryptedCLIHomeVaultWithKeyring(
		f.accounts,
		f.store,
		map[string]*pkgcrypto.Cipher{"v1": f.cipher},
		"v2",
		f.runtimeBase,
	)
	require.Error(t, err, "the current key version must exist in the keyring")

	_, err = NewEncryptedCLIHomeVaultWithKeyring(
		f.accounts,
		f.store,
		map[string]*pkgcrypto.Cipher{"v1": nil},
		"v1",
		f.runtimeBase,
	)
	require.Error(t, err, "nil ciphers must fail closed")
}

func TestEncryptedCLIHomeVault_CleanupRuntimeHomesAtStartup(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	staleDir := filepath.Join(f.runtimeBase, cliHomeRuntimePrefix+"stale")
	staleFile := filepath.Join(f.runtimeBase, cliHomeRuntimePrefix+"partial")
	keepFile := filepath.Join(f.runtimeBase, "keep.txt")
	keepDir := filepath.Join(f.runtimeBase, "persistent")
	require.NoError(t, os.Mkdir(staleDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(staleDir, "secret"), []byte("plaintext"), 0o600))
	require.NoError(t, os.WriteFile(staleFile, []byte("partial"), 0o600))
	require.NoError(t, os.WriteFile(keepFile, []byte("keep"), 0o600))
	require.NoError(t, os.Mkdir(keepDir, 0o700))

	require.NoError(t, f.vault.CleanupRuntimeHomesAtStartup())
	require.NoFileExists(t, staleDir)
	require.NoFileExists(t, staleFile)
	require.FileExists(t, keepFile)
	require.DirExists(t, keepDir)
}

func TestEncryptedCLIHomeVault_CleanupRuntimeHomesAtStartupRejectsSymlinkBase(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	outside := t.TempDir()
	outsideStale := filepath.Join(outside, cliHomeRuntimePrefix+"outside")
	require.NoError(t, os.Mkdir(outsideStale, 0o700))
	runtimeLink := filepath.Join(t.TempDir(), "runtime-link")
	require.NoError(t, os.Symlink(outside, runtimeLink))
	vault, err := NewEncryptedCLIHomeVault(f.accounts, f.store, f.cipher, "v1", runtimeLink)
	require.NoError(t, err)

	require.Error(t, vault.CleanupRuntimeHomesAtStartup())
	require.DirExists(t, outsideStale)
}

type task2CloseErrorFile struct {
	*os.File
	closeErr error
}

func (f *task2CloseErrorFile) Close() error {
	_ = f.File.Close()
	return f.closeErr
}

func TestWriteCLIHomeTarOpenedFile_PreservesCloseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"apps":[]}`), 0o600))
	file, err := os.Open(path)
	require.NoError(t, err)
	info, err := file.Stat()
	require.NoError(t, err)
	closeErr := errors.New("close failed")
	wrapped := &task2CloseErrorFile{File: file, closeErr: closeErr}
	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)

	err = writeCLIHomeTarOpenedFile(writer, wrapped, "config.json", info)
	require.ErrorIs(t, err, closeErr)
}

func TestEncryptedCLIHomeVault_RuntimePermissionsCleanupAndRevisionCAS(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)

	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		require.Equal(t, f.runtimeBase, f.vault.RuntimeBase())
		task2RequireMode(t, home, 0o700)
		require.NoError(t, os.Mkdir(filepath.Join(home, "nested"), 0o777))
		require.NoError(t, os.WriteFile(filepath.Join(home, "nested", "config.json"), []byte(`{"apps":[]}`), 0o666))
		return true, nil
	})
	require.NoError(t, err)
	task2RequireMode(t, f.runtimeBase, 0o700)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
	require.Equal(t, []uint64{0}, f.store.putExpected)
	require.Equal(t, uint64(1), f.store.vaults[f.userID].Revision)
	require.Equal(t, task2CiphertextChecksum(f.store.vaults[f.userID].Ciphertext), f.store.vaults[f.userID].Checksum)

	putCalls := f.store.putCalls
	err = f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		task2RequireMode(t, home, 0o700)
		task2RequireMode(t, filepath.Join(home, "nested"), 0o700)
		task2RequireMode(t, filepath.Join(home, "nested", "config.json"), 0o600)
		body, readErr := os.ReadFile(filepath.Join(home, "nested", "config.json"))
		require.NoError(t, readErr)
		require.JSONEq(t, `{"apps":[]}`, string(body))
		return false, nil
	})
	require.NoError(t, err)
	require.Equal(t, putCalls, f.store.putCalls, "changed=false must not write a new snapshot")
	task2RequireNoRuntimeHomes(t, f.runtimeBase)

	err = f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		return true, os.WriteFile(filepath.Join(home, "nested", "config.json"), []byte(`{"apps":["next"]}`), 0o600)
	})
	require.NoError(t, err)
	require.Equal(t, []uint64{0, 1}, f.store.putExpected)
	require.Equal(t, uint64(2), f.store.vaults[f.userID].Revision)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_ChangedFalseDoesNotCreateVault(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)

	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		require.NoError(t, os.WriteFile(filepath.Join(home, "ignored"), []byte("ephemeral"), 0o600))
		return false, nil
	})
	require.NoError(t, err)
	require.Empty(t, f.store.vaults)
	require.Zero(t, f.store.putCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_CallbackErrorStillCleansUp(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	callbackErr := errors.New("runner failed")

	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		require.NoError(t, os.WriteFile(filepath.Join(home, "secret"), []byte("token"), 0o600))
		return true, callbackErr
	})
	require.ErrorIs(t, err, callbackErr)
	require.Zero(t, f.store.putCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_RejectsInactiveGenerationBeforeReadingVault(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 2)

	err := f.vault.WithHome(context.Background(), f.userID, 1, func(string) (bool, error) {
		t.Fatal("callback must not run for an inactive generation")
		return false, nil
	})
	require.Error(t, err)
	require.Zero(t, f.store.getCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_WithRetiredHomeOnlyAllowsDisconnectingNewerGenerationAndNeverReseals(t *testing.T) {
	fixture := newTask2VaultFixture(t, 7, 4)
	require.NoError(t, fixture.vault.WithHome(context.Background(), 7, 4, func(home string) (bool, error) {
		return true, os.WriteFile(filepath.Join(home, "state.json"), []byte(`{"token":"local"}`), 0o600)
	}))
	putsBefore := fixture.store.putCalls
	fixture.accounts.accounts[7].Generation = 5
	fixture.accounts.accounts[7].ConnectionState = model.FeishuConnectionDisconnecting

	var contents []byte
	err := fixture.vault.WithRetiredHome(context.Background(), 7, 4, func(home string) error {
		contents, _ = os.ReadFile(filepath.Join(home, "state.json"))
		return nil
	})
	require.NoError(t, err)
	require.JSONEq(t, `{"token":"local"}`, string(contents))
	require.Equal(t, putsBefore, fixture.store.putCalls, "retired teardown must never reseal a deleted generation")

	fixture.accounts.accounts[7].ConnectionState = model.FeishuConnectionConnected
	require.Error(t, fixture.vault.WithRetiredHome(context.Background(), 7, 4, func(string) error { return nil }))
}

func TestEncryptedCLIHomeVault_RejectsChecksumTampering(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	f.putEncryptedArchive(t, task2TarArchive(t, task2TarEntry{
		name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"apps":[]}`),
	}), 1)
	f.store.vaults[f.userID].Checksum = "tampered-checksum"

	callbackCalled := false
	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(string) (bool, error) {
		callbackCalled = true
		return false, nil
	})
	require.Error(t, err)
	require.False(t, callbackCalled)
	require.Zero(t, f.store.putCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_AADRejectsOtherUserAndGeneration(t *testing.T) {
	original := newTask2VaultFixture(t, 7, 1)
	archive := task2TarArchive(t, task2TarEntry{
		name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"token":"secret"}`),
	})
	originalAAD := []byte("lark|7|1|v1")
	ciphertext, err := original.cipher.EncryptWithAAD(archive, originalAAD)
	require.NoError(t, err)

	for _, tc := range []struct {
		name       string
		userID     uint
		generation uint64
	}{
		{name: "other user", userID: 8, generation: 1},
		{name: "other generation", userID: 7, generation: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTask2VaultFixture(t, tc.userID, tc.generation)
			// Use the same encryption key but bind the row to the requested tenant/generation.
			f.cipher = original.cipher
			var constructorErr error
			f.vault, constructorErr = NewEncryptedCLIHomeVault(f.accounts, f.store, f.cipher, f.keyVersion, f.runtimeBase)
			require.NoError(t, constructorErr)
			f.store.vaults[tc.userID] = &model.FeishuCLIVault{
				UserID: tc.userID, Generation: tc.generation, Ciphertext: append([]byte(nil), ciphertext...),
				KeyVersion: "v1", Checksum: task2CiphertextChecksum(ciphertext), Revision: 1,
			}

			callbackCalled := false
			err := f.vault.WithHome(context.Background(), tc.userID, tc.generation, func(string) (bool, error) {
				callbackCalled = true
				return false, nil
			})
			require.Error(t, err)
			require.False(t, callbackCalled)
			task2RequireNoRuntimeHomes(t, f.runtimeBase)
		})
	}
}

func TestEncryptedCLIHomeVault_RejectsUnsafeTarEntriesWithoutEscaping(t *testing.T) {
	absOutside := filepath.Join(t.TempDir(), "absolute-escape")
	for _, tc := range []struct {
		name  string
		entry task2TarEntry
	}{
		{name: "parent traversal", entry: task2TarEntry{name: "../escape", typeflag: tar.TypeReg, body: []byte("pwned")}},
		{name: "absolute path", entry: task2TarEntry{name: absOutside, typeflag: tar.TypeReg, body: []byte("pwned")}},
		{name: "symbolic link", entry: task2TarEntry{name: "link", typeflag: tar.TypeSymlink, linkname: absOutside}},
		{name: "hard link", entry: task2TarEntry{name: "hard", typeflag: tar.TypeLink, linkname: absOutside}},
		{name: "fifo", entry: task2TarEntry{name: "pipe", typeflag: tar.TypeFifo}},
		{name: "character device", entry: task2TarEntry{name: "device", typeflag: tar.TypeChar}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTask2VaultFixture(t, 7, 1)
			f.putEncryptedArchive(t, task2TarArchive(t, tc.entry), 1)
			callbackCalled := false
			err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(string) (bool, error) {
				callbackCalled = true
				return false, nil
			})
			require.Error(t, err)
			require.False(t, callbackCalled)
			require.NoFileExists(t, absOutside)
			require.NoFileExists(t, filepath.Join(f.runtimeBase, "escape"))
			task2RequireNoRuntimeHomes(t, f.runtimeBase)
		})
	}
}

func TestEncryptedCLIHomeVault_SafePackingRejectsSymlink(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	outside := filepath.Join(t.TempDir(), "outside-secret")
	require.NoError(t, os.WriteFile(outside, []byte("do not archive"), 0o600))

	err := f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		return true, os.Symlink(outside, filepath.Join(home, "linked-secret"))
	})
	require.Error(t, err)
	require.Zero(t, f.store.putCalls)
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}

func TestEncryptedCLIHomeVault_CASConflictDoesNotOverwriteNewerSnapshot(t *testing.T) {
	f := newTask2VaultFixture(t, 7, 1)
	f.putEncryptedArchive(t, task2TarArchive(t, task2TarEntry{
		name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"revision":1}`),
	}), 1)
	newerCiphertext, err := f.cipher.EncryptWithAAD(
		task2TarArchive(t, task2TarEntry{
			name: "config.json", typeflag: tar.TypeReg, body: []byte(`{"revision":2}`),
		}),
		[]byte("lark|7|1|v1"),
	)
	require.NoError(t, err)
	newer := &model.FeishuCLIVault{
		UserID: 7, Generation: 1, Ciphertext: newerCiphertext, KeyVersion: "v1",
		Checksum: task2CiphertextChecksum(newerCiphertext), Revision: 2,
	}
	f.store.beforePut = func() {
		f.store.vaults[f.userID] = cloneTask2Vault(newer)
	}
	f.store.putErr = gorm.ErrRecordNotFound

	err = f.vault.WithHome(context.Background(), f.userID, f.generation, func(home string) (bool, error) {
		return true, os.WriteFile(filepath.Join(home, "config.json"), []byte(`{"revision":"stale-write"}`), 0o600)
	})
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
	require.Equal(t, []uint64{1}, f.store.putExpected)
	require.Equal(t, newer, f.store.vaults[f.userID], "failed CAS must leave the newer persisted snapshot untouched")
	task2RequireNoRuntimeHomes(t, f.runtimeBase)
}
