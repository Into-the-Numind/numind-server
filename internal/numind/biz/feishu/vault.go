package feishu

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	pkgcrypto "numind-server/internal/pkg/crypto"
	"numind-server/internal/pkg/model"
)

const (
	cliHomeRuntimePrefix      = "lark-home-"
	maxCLIHomeCiphertextBytes = 64 << 20
	maxCLIHomeArchiveBytes    = 64 << 20
	maxCLIHomeArchiveEntries  = 10_000
)

// CLIHomeAccountStore is the account metadata subset needed to fence vault
// access to the active lark connection generation.
type CLIHomeAccountStore interface {
	Get(ctx context.Context, userID uint, provider string) (*model.UserThirdPartyAccount, error)
}

// CLIHomeSnapshotStore is the persistence subset needed to load and
// compare-and-swap one encrypted HOME snapshot.
type CLIHomeSnapshotStore interface {
	GetVault(ctx context.Context, userID uint, generation uint64) (*model.FeishuCLIVault, error)
	PutVaultCAS(ctx context.Context, vault *model.FeishuCLIVault, expectedRevision uint64) error
}

// EncryptedCLIHomeVault materializes one lark-cli HOME only for the duration of
// a callback and persists changed snapshots using authenticated encryption.
type EncryptedCLIHomeVault struct {
	accounts    CLIHomeAccountStore
	snapshots   CLIHomeSnapshotStore
	cipher      *pkgcrypto.Cipher
	keyVersion  string
	runtimeBase string
}

// NewEncryptedCLIHomeVault constructs a generation-fenced encrypted HOME vault.
func NewEncryptedCLIHomeVault(
	accounts CLIHomeAccountStore,
	snapshots CLIHomeSnapshotStore,
	cipher *pkgcrypto.Cipher,
	keyVersion string,
	runtimeBase string,
) (*EncryptedCLIHomeVault, error) {
	if accounts == nil {
		return nil, errors.New("feishu CLI home vault: nil account store")
	}
	if snapshots == nil {
		return nil, errors.New("feishu CLI home vault: nil snapshot store")
	}
	if cipher == nil {
		return nil, errors.New("feishu CLI home vault: nil cipher")
	}
	if keyVersion == "" || strings.Contains(keyVersion, "|") {
		return nil, errors.New("feishu CLI home vault: invalid key version")
	}
	if runtimeBase == "" {
		return nil, errors.New("feishu CLI home vault: empty runtime base")
	}
	absRuntimeBase, err := filepath.Abs(runtimeBase)
	if err != nil {
		return nil, fmt.Errorf("feishu CLI home vault: resolve runtime base: %w", err)
	}
	return &EncryptedCLIHomeVault{
		accounts:    accounts,
		snapshots:   snapshots,
		cipher:      cipher,
		keyVersion:  keyVersion,
		runtimeBase: absRuntimeBase,
	}, nil
}

// RuntimeBase returns the parent directory used for short-lived plaintext
// HOME directories. Callers may use it for startup cleanup of abandoned homes.
func (v *EncryptedCLIHomeVault) RuntimeBase() string {
	return v.runtimeBase
}

// WithHome opens the active user's encrypted HOME, invokes callback, and only
// writes a new CAS revision when callback reports changed=true.
func (v *EncryptedCLIHomeVault) WithHome(
	ctx context.Context,
	userID uint,
	generation uint64,
	callback func(home string) (changed bool, err error),
) (retErr error) {
	if callback == nil {
		return errors.New("feishu CLI home vault: nil callback")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("feishu CLI home vault: context unavailable: %w", err)
	}

	account, err := v.accounts.Get(ctx, userID, ProviderLark)
	if err != nil {
		return fmt.Errorf("feishu CLI home vault: read active account: %w", err)
	}
	if account.UserID != userID || account.Provider != ProviderLark || account.Generation != generation {
		return fmt.Errorf("feishu CLI home vault: inactive account generation: %w", gorm.ErrRecordNotFound)
	}

	snapshot, err := v.snapshots.GetVault(ctx, userID, generation)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("feishu CLI home vault: read snapshot: %w", err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		snapshot = nil
	}

	var archive []byte
	if snapshot != nil {
		archive, err = v.openSnapshot(userID, generation, snapshot)
		if err != nil {
			return err
		}
	}

	if err := ensureCLIHomeRuntimeBase(v.runtimeBase); err != nil {
		return err
	}
	home, err := os.MkdirTemp(v.runtimeBase, cliHomeRuntimePrefix)
	if err != nil {
		return fmt.Errorf("feishu CLI home vault: create temporary HOME: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(home); cleanupErr != nil {
			wrapped := fmt.Errorf("feishu CLI home vault: remove temporary HOME: %w", cleanupErr)
			if retErr == nil {
				retErr = wrapped
			} else {
				retErr = errors.Join(retErr, wrapped)
			}
		}
	}()
	if err := os.Chmod(home, 0o700); err != nil {
		return fmt.Errorf("feishu CLI home vault: restrict temporary HOME: %w", err)
	}
	if snapshot != nil {
		if err := unpackCLIHome(archive, home); err != nil {
			return fmt.Errorf("feishu CLI home vault: unpack snapshot: %w", err)
		}
	}

	changed, err := callback(home)
	if err != nil {
		return fmt.Errorf("feishu CLI home vault: callback failed: %w", err)
	}
	if !changed {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("feishu CLI home vault: context ended before sealing: %w", err)
	}

	archive, err = packCLIHome(home)
	if err != nil {
		return fmt.Errorf("feishu CLI home vault: pack snapshot: %w", err)
	}
	aad := cliHomeAAD(userID, generation, v.keyVersion)
	ciphertext, err := v.cipher.EncryptWithAAD(archive, aad)
	if err != nil {
		return fmt.Errorf("feishu CLI home vault: encrypt snapshot: %w", err)
	}
	if len(ciphertext) > maxCLIHomeCiphertextBytes {
		return fmt.Errorf("feishu CLI home vault: encrypted snapshot exceeds %d bytes", maxCLIHomeCiphertextBytes)
	}

	expectedRevision := uint64(0)
	if snapshot != nil {
		expectedRevision = snapshot.Revision
	}
	candidate := &model.FeishuCLIVault{
		UserID:     userID,
		Generation: generation,
		Ciphertext: ciphertext,
		KeyVersion: v.keyVersion,
		Checksum:   cliHomeCiphertextChecksum(ciphertext),
		Revision:   expectedRevision + 1,
	}
	if err := v.snapshots.PutVaultCAS(ctx, candidate, expectedRevision); err != nil {
		return fmt.Errorf("feishu CLI home vault: persist snapshot revision: %w", err)
	}
	return nil
}

func (v *EncryptedCLIHomeVault) openSnapshot(
	userID uint,
	generation uint64,
	snapshot *model.FeishuCLIVault,
) ([]byte, error) {
	if snapshot.UserID != userID || snapshot.Generation != generation {
		return nil, fmt.Errorf("feishu CLI home vault: snapshot ownership mismatch: %w", gorm.ErrRecordNotFound)
	}
	if snapshot.Revision == 0 {
		return nil, errors.New("feishu CLI home vault: persisted snapshot has zero revision")
	}
	if snapshot.KeyVersion == "" || strings.Contains(snapshot.KeyVersion, "|") {
		return nil, errors.New("feishu CLI home vault: snapshot has invalid key version")
	}
	if len(snapshot.Ciphertext) > maxCLIHomeCiphertextBytes {
		return nil, fmt.Errorf("feishu CLI home vault: encrypted snapshot exceeds %d bytes", maxCLIHomeCiphertextBytes)
	}
	checksum := cliHomeCiphertextChecksum(snapshot.Ciphertext)
	if len(snapshot.Checksum) != len(checksum) ||
		subtle.ConstantTimeCompare([]byte(snapshot.Checksum), []byte(checksum)) != 1 {
		return nil, errors.New("feishu CLI home vault: ciphertext checksum mismatch")
	}
	archive, err := v.cipher.DecryptWithAAD(
		snapshot.Ciphertext,
		cliHomeAAD(userID, generation, snapshot.KeyVersion),
	)
	if err != nil {
		return nil, fmt.Errorf("feishu CLI home vault: decrypt snapshot: %w", err)
	}
	if len(archive) > maxCLIHomeArchiveBytes {
		return nil, fmt.Errorf("feishu CLI home vault: decrypted snapshot exceeds %d bytes", maxCLIHomeArchiveBytes)
	}
	return archive, nil
}

func cliHomeAAD(userID uint, generation uint64, keyVersion string) []byte {
	return []byte(fmt.Sprintf("lark|%d|%d|%s", userID, generation, keyVersion))
}

func cliHomeCiphertextChecksum(ciphertext []byte) string {
	sum := sha256.Sum256(ciphertext)
	return hex.EncodeToString(sum[:])
}

func ensureCLIHomeRuntimeBase(runtimeBase string) error {
	if err := os.MkdirAll(runtimeBase, 0o700); err != nil {
		return fmt.Errorf("feishu CLI home vault: create runtime base: %w", err)
	}
	info, err := os.Lstat(runtimeBase)
	if err != nil {
		return fmt.Errorf("feishu CLI home vault: inspect runtime base: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("feishu CLI home vault: runtime base is not a real directory")
	}
	if err := os.Chmod(runtimeBase, 0o700); err != nil {
		return fmt.Errorf("feishu CLI home vault: restrict runtime base: %w", err)
	}
	return nil
}

func unpackCLIHome(archive []byte, root string) error {
	if len(archive) > maxCLIHomeArchiveBytes {
		return fmt.Errorf("archive exceeds %d bytes", maxCLIHomeArchiveBytes)
	}
	reader := tar.NewReader(bytes.NewReader(archive))
	entries := 0
	var totalFileBytes int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar header: %w", err)
		}
		entries++
		if entries > maxCLIHomeArchiveEntries {
			return fmt.Errorf("archive exceeds %d entries", maxCLIHomeArchiveEntries)
		}

		destination, err := secureCLIHomeDestination(root, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := secureCLIHomeMkdirAll(root, destination); err != nil {
				return fmt.Errorf("create archive directory: %w", err)
			}
		case tar.TypeReg, 0: // NUL is the legacy tar encoding for a regular file.
			if header.Size < 0 || header.Size > maxCLIHomeArchiveBytes ||
				totalFileBytes > int64(maxCLIHomeArchiveBytes)-header.Size {
				return fmt.Errorf("archive regular files exceed %d bytes", maxCLIHomeArchiveBytes)
			}
			totalFileBytes += header.Size
			if err := secureCLIHomeMkdirAll(root, filepath.Dir(destination)); err != nil {
				return fmt.Errorf("create archive parent directory: %w", err)
			}
			if err := extractCLIHomeRegularFile(reader, destination, header.Size); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry type %d is not a regular file or directory", header.Typeflag)
		}
	}
}

func secureCLIHomeDestination(root, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, '\x00') {
		return "", errors.New("archive contains an invalid empty or NUL path")
	}
	nativeName := filepath.FromSlash(name)
	if path.IsAbs(name) || filepath.IsAbs(nativeName) || filepath.VolumeName(nativeName) != "" {
		return "", errors.New("archive contains an absolute path")
	}
	cleanName := filepath.Clean(nativeName)
	if cleanName == "." {
		return "", errors.New("archive entry must be strictly inside HOME")
	}
	destination := filepath.Join(root, cleanName)
	relative, err := filepath.Rel(root, destination)
	if err != nil {
		return "", fmt.Errorf("resolve archive path: %w", err)
	}
	if relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("archive path escapes HOME")
	}
	return destination, nil
}

func secureCLIHomeMkdirAll(root, target string) error {
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("resolve directory path: %w", err)
	}
	if relative == "." {
		return os.Chmod(root, 0o700)
	}
	if relative == ".." || filepath.IsAbs(relative) ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("directory path escapes HOME")
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return errors.New("directory path contains an invalid component")
		}
		current = filepath.Join(current, component)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, fs.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("create protected directory: %w", err)
			}
			continue
		}
		if statErr != nil {
			return fmt.Errorf("inspect protected directory: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("archive parent is not a real directory")
		}
		if err := os.Chmod(current, 0o700); err != nil {
			return fmt.Errorf("restrict archive directory: %w", err)
		}
	}
	return nil
}

func extractCLIHomeRegularFile(reader io.Reader, destination string, size int64) error {
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create archive regular file: %w", err)
	}
	written, copyErr := io.CopyN(file, reader, size)
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("extract archive regular file: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("close archive regular file: %w", closeErr)
	}
	if written != size {
		_ = os.Remove(destination)
		return fmt.Errorf("archive regular file size mismatch: wrote %d of %d", written, size)
	}
	if err := os.Chmod(destination, 0o600); err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("restrict archive regular file: %w", err)
	}
	return nil
}

func packCLIHome(root string) ([]byte, error) {
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return nil, fmt.Errorf("inspect HOME root: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("HOME root is not a real directory")
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("restrict HOME root: %w", err)
	}

	var buffer bytes.Buffer
	writer := tar.NewWriter(&buffer)
	entries := 0
	var totalFileBytes int64
	walkErr := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk HOME: %w", walkErr)
		}
		if filePath == root {
			return nil
		}
		entries++
		if entries > maxCLIHomeArchiveEntries {
			return fmt.Errorf("HOME exceeds %d entries", maxCLIHomeArchiveEntries)
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect HOME entry: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("HOME contains a symbolic link")
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return fmt.Errorf("resolve HOME entry path: %w", err)
		}
		if relative == "." || relative == ".." || filepath.IsAbs(relative) ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("HOME entry escapes root")
		}

		switch {
		case info.IsDir():
			if err := os.Chmod(filePath, 0o700); err != nil {
				return fmt.Errorf("restrict HOME directory: %w", err)
			}
			fresh, err := os.Lstat(filePath)
			if err != nil || !fresh.IsDir() || fresh.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, fresh) {
				return errors.New("HOME directory changed while packing")
			}
			return writeCLIHomeTarHeader(writer, fresh, filepath.ToSlash(relative), 0o700)
		case info.Mode().IsRegular():
			if err := os.Chmod(filePath, 0o600); err != nil {
				return fmt.Errorf("restrict HOME regular file: %w", err)
			}
			fresh, err := os.Lstat(filePath)
			if err != nil || !fresh.Mode().IsRegular() || !os.SameFile(info, fresh) {
				return errors.New("HOME regular file changed while packing")
			}
			if fresh.Size() < 0 || fresh.Size() > maxCLIHomeArchiveBytes ||
				totalFileBytes > int64(maxCLIHomeArchiveBytes)-fresh.Size() {
				return fmt.Errorf("HOME regular files exceed %d bytes", maxCLIHomeArchiveBytes)
			}
			totalFileBytes += fresh.Size()
			return writeCLIHomeTarFile(writer, filePath, relative, fresh)
		default:
			return errors.New("HOME contains a non-regular entry")
		}
	})
	if walkErr != nil {
		_ = writer.Close()
		return nil, walkErr
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close HOME archive: %w", err)
	}
	if buffer.Len() > maxCLIHomeArchiveBytes {
		return nil, fmt.Errorf("HOME archive exceeds %d bytes", maxCLIHomeArchiveBytes)
	}
	return buffer.Bytes(), nil
}

func writeCLIHomeTarHeader(writer *tar.Writer, info fs.FileInfo, name string, mode int64) error {
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return fmt.Errorf("build HOME archive header: %w", err)
	}
	header.Name = name
	header.Mode = mode
	header.Uid = 0
	header.Gid = 0
	header.Uname = ""
	header.Gname = ""
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("write HOME archive header: %w", err)
	}
	return nil
}

func writeCLIHomeTarFile(writer *tar.Writer, filePath, relative string, info fs.FileInfo) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open HOME regular file: %w", err)
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened HOME regular file: %w", err)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return errors.New("HOME regular file changed before opening")
	}
	if err := writeCLIHomeTarHeader(writer, openedInfo, filepath.ToSlash(relative), 0o600); err != nil {
		return err
	}
	if _, err := io.CopyN(writer, file, openedInfo.Size()); err != nil {
		return fmt.Errorf("write HOME regular file: %w", err)
	}
	return nil
}
