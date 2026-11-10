package secrets

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Master key files in the data directory.
const (
	// KeyFileName is the master key: exactly 32 random bytes, mode 0600.
	KeyFileName = "master.key"
	// newKeySuffix marks the next key while a rotation is in progress.
	newKeySuffix = ".new"
	// oldKeySuffix marks the previous key, kept after a rotation until the
	// next successful start.
	oldKeySuffix = ".old"
)

// readKeyFile reads a key file, refusing one that is not exactly KeySize
// bytes or that grants any permission to group or other. A missing file is
// reported as an error matching fs.ErrNotExist.
func readKeyFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // the path is <data-dir>/master.key, chosen by the operator's flag
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("secrets: stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s is not a regular file", ErrKeyFile, path)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s has mode %#o; it must not be accessible to group or other (chmod 600)", ErrKeyFile, path, perm)
	}
	key, err := io.ReadAll(io.LimitReader(f, KeySize+1))
	if err != nil {
		return nil, fmt.Errorf("secrets: read %s: %w", path, err)
	}
	if len(key) != KeySize {
		return nil, fmt.Errorf("%w: %s must be exactly %d bytes", ErrKeyFile, path, KeySize)
	}
	return key, nil
}

func newRandomKey() ([]byte, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("secrets: generate key: %w", err)
	}
	return key, nil
}

// createKeyFile creates a new random key at path with create-exclusive, mode
// 0600. If another process created the file first, its key is read instead,
// so two processes can never both create one.
func createKeyFile(path string) ([]byte, error) {
	key, err := newRandomKey()
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // see readKeyFile
	if errors.Is(err, fs.ErrExist) {
		return readKeyFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("secrets: create %s: %w", path, err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secrets: write %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("secrets: sync %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("secrets: close %s: %w", path, err)
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return key, nil
}

// writeKeyFileAtomic writes key to path through a temporary file in the same
// directory: write, fsync, rename, fsync the directory. A crash leaves either
// the old file or the new one, never a partial key.
func writeKeyFileAtomic(path string, key []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("secrets: create temporary key file: %w", err)
	}
	tmp := f.Name()
	cleanup := func() { _ = os.Remove(tmp) }
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("secrets: chmod temporary key file: %w", err)
	}
	if _, err := f.Write(key); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("secrets: write temporary key file: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		cleanup()
		return fmt.Errorf("secrets: sync temporary key file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return fmt.Errorf("secrets: close temporary key file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		cleanup()
		return fmt.Errorf("secrets: install %s: %w", path, err)
	}
	return syncDir(dir)
}

// renameKeyFile atomically moves from to to and makes the rename durable.
func renameKeyFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return fmt.Errorf("secrets: rename %s: %w", from, err)
	}
	return syncDir(filepath.Dir(to))
}

// removeIfExists deletes path, ignoring a missing file.
func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secrets: remove %s: %w", path, err)
	}
	return nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir) //nolint:gosec // the data directory
	if err != nil {
		return fmt.Errorf("secrets: open %s: %w", dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("secrets: sync %s: %w", dir, err)
	}
	return nil
}
