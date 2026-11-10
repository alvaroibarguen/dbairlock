package secrets

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/secrets/secretsdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Transactor is the store surface the secret store needs.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// Options configure the secret store.
type Options struct {
	// DataDir holds master.key.
	DataDir string
	Store   Transactor
	Audit   Auditor
	Clock   clock.Clock
	IDs     ids.Generator
	Logger  *slog.Logger
}

func (o Options) validate() error {
	if o.DataDir == "" || o.Store == nil || o.Audit == nil || o.Clock == nil || o.IDs == nil || o.Logger == nil {
		return errors.New("secrets: data directory, store, audit, clock, ids and logger are required")
	}
	return nil
}

// Service is the secret store. It implements both Writer and Opener; the
// composition root hands each module only the interface it may hold.
type Service struct {
	opts Options
	key  cipherKey
}

var (
	_ Writer = (*Service)(nil)
	_ Opener = (*Service)(nil)
)

// Load loads the master key and proves it belongs to the database:
//
//   - If master.key is missing and the database holds no key check, a new key
//     is created (create-exclusive, mode 0600) and its check value stored.
//   - If master.key is missing but the database has a key check, it refuses:
//     the secrets were encrypted with a key that must be restored.
//   - It refuses a key file that is not exactly 32 bytes or that grants any
//     permission to group or other.
//   - It refuses a key that cannot decrypt the stored check value, unless an
//     interrupted rotation left the matching key in master.key.new, in which
//     case it finishes the rotation.
//
// After a successful start master.key.old, kept by the last rotation, is
// removed.
func Load(ctx context.Context, opts Options) (*Service, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}
	key, err := loadKey(ctx, opts)
	if err != nil {
		return nil, err
	}
	path := keyPath(opts.DataDir)
	if err := removeIfExists(path + oldKeySuffix); err != nil {
		return nil, err
	}
	return &Service{opts: opts, key: key}, nil
}

func keyPath(dataDir string) string { return filepath.Join(dataDir, KeyFileName) }

// loadKey implements the start-up rules of Load, without removing the old key.
func loadKey(ctx context.Context, opts Options) (cipherKey, error) {
	path := keyPath(opts.DataDir)
	q := secretsdb.New(opts.Store.Reader())
	check, err := q.GetKeyCheck(ctx)
	hasCheck := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return cipherKey{}, fmt.Errorf("secrets: read key check: %w", err)
	}

	raw, err := readKeyFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && hasCheck:
		return cipherKey{}, fmt.Errorf("%w: %s is missing, but this database holds secrets encrypted with a master key; restore master.key from your backup", ErrKeyMismatch, path)
	case errors.Is(err, fs.ErrNotExist):
		if raw, err = createKeyFile(path); err != nil {
			return cipherKey{}, err
		}
		opts.Logger.Info("created a new master key", slog.String("path", path))
	case err != nil:
		return cipherKey{}, err
	}

	if !hasCheck {
		key, err := newCipherKey(raw, 1)
		if err != nil {
			return cipherKey{}, err
		}
		if err := initKeyCheck(ctx, opts, key); err != nil {
			return cipherKey{}, err
		}
		if err := removeIfExists(path + newKeySuffix); err != nil {
			return cipherKey{}, err
		}
		return key, nil
	}

	key, err := newCipherKey(raw, check.KeyVersion)
	if err != nil {
		return cipherKey{}, err
	}
	if verifyKeyCheck(key, check) {
		// A leftover master.key.new is from a rotation that never committed.
		if err := removeIfExists(path + newKeySuffix); err != nil {
			return cipherKey{}, err
		}
		return key, nil
	}

	// A rotation that committed but crashed before installing the new key
	// leaves the matching key in master.key.new: finish it.
	if next, err := readKeyFile(path + newKeySuffix); err == nil {
		if nk, err := newCipherKey(next, check.KeyVersion); err == nil && verifyKeyCheck(nk, check) {
			if err := renameKeyFile(path+newKeySuffix, path); err != nil {
				return cipherKey{}, err
			}
			opts.Logger.Warn("finished an interrupted master key rotation", slog.Int64("key_version", check.KeyVersion))
			return nk, nil
		}
	}
	return cipherKey{}, fmt.Errorf("%w: %s cannot decrypt this database's key check; it was created for another database, or the file was replaced", ErrKeyMismatch, path)
}

func verifyKeyCheck(k cipherKey, check secretsdb.SecretKeyCheck) bool {
	pt, err := k.open(check.Ciphertext, check.Nonce, keyCheckAD())
	return err == nil && string(pt) == keyCheckPlaintext
}

func initKeyCheck(ctx context.Context, opts Options, key cipherKey) error {
	ct, nonce, err := key.seal([]byte(keyCheckPlaintext), keyCheckAD())
	if err != nil {
		return err
	}
	actx := audit.WithActor(ctx, audit.SystemActor)
	return opts.Store.Tx(actx, func(tx *store.Tx) error {
		err := secretsdb.New(tx).PutKeyCheck(actx, secretsdb.PutKeyCheckParams{
			Ciphertext: ct, Nonce: nonce, KeyVersion: key.version, UpdatedAt: opts.Clock.Now().UnixMilli(),
		})
		if err != nil {
			return fmt.Errorf("secrets: store key check: %w", err)
		}
		return opts.Audit.Record(actx, tx, audit.Event{
			Action:      audit.MasterKeyInitialized,
			SubjectType: "master_key",
			Payload:     map[string]any{"key_version": key.version},
		})
	})
}

// KeyVersion is the version of the master key in use; rotation increments it.
func (s *Service) KeyVersion() int64 { return s.key.version }

func checkValue(v Value) error {
	if v.IsEmpty() {
		return fmt.Errorf("%w: a secret cannot be empty", ErrInvalidValue)
	}
	if len(v.b) > MaxValueBytes {
		return fmt.Errorf("%w: a secret cannot be longer than %d bytes", ErrInvalidValue, MaxValueBytes)
	}
	return nil
}

// Set implements Writer.
func (s *Service) Set(ctx context.Context, tx *store.Tx, ref Ref, plaintext Value) error {
	if err := ref.validate(); err != nil {
		return err
	}
	if err := checkValue(plaintext); err != nil {
		return err
	}
	actor, ok := audit.ActorFrom(ctx)
	if !ok {
		return audit.ErrNoActor
	}
	q := secretsdb.New(tx)
	row, err := q.GetSecret(ctx, getParams(ref))
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("secrets: look up %s: %w", ref, err)
	}
	id := row.ID
	if !exists {
		id = s.opts.IDs.NewID()
	}
	ct, nonce, err := s.key.seal(plaintext.b, secretAD(id, ref.Kind, ref.OwnerType, ref.OwnerID))
	if err != nil {
		return err
	}
	now := s.opts.Clock.Now().UnixMilli()
	action := audit.SecretSet
	if exists {
		action = audit.SecretReplaced
		err = q.ReplaceSecret(ctx, secretsdb.ReplaceSecretParams{
			Ciphertext: ct, Nonce: nonce, KeyVersion: s.key.version,
			SetBy: actor.Label(), SetAt: now, ID: id,
		})
	} else {
		err = q.InsertSecret(ctx, secretsdb.InsertSecretParams{
			ID: id, Kind: string(ref.Kind), OwnerType: ref.OwnerType, OwnerID: ref.OwnerID,
			Ciphertext: ct, Nonce: nonce, KeyVersion: s.key.version,
			SetBy: actor.Label(), SetAt: now,
		})
	}
	if err != nil {
		return fmt.Errorf("secrets: store %s: %w", ref, err)
	}
	return s.opts.Audit.Record(ctx, tx, audit.Event{
		// The payload is the reference, never the value.
		Action: action, SubjectType: "secret", SubjectID: id, Payload: ref,
	})
}

// Clear implements Writer. Clearing a secret that is not set changes nothing
// and records nothing.
func (s *Service) Clear(ctx context.Context, tx *store.Tx, ref Ref) error {
	if err := ref.validate(); err != nil {
		return err
	}
	q := secretsdb.New(tx)
	row, err := q.GetSecret(ctx, getParams(ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("secrets: look up %s: %w", ref, err)
	}
	if err := q.DeleteSecret(ctx, row.ID); err != nil {
		return fmt.Errorf("secrets: clear %s: %w", ref, err)
	}
	return s.opts.Audit.Record(ctx, tx, audit.Event{
		Action: audit.SecretCleared, SubjectType: "secret", SubjectID: row.ID, Payload: ref,
	})
}

// Describe implements Writer.
func (s *Service) Describe(ctx context.Context, ref Ref) (Meta, error) {
	if err := ref.validate(); err != nil {
		return Meta{}, err
	}
	row, err := secretsdb.New(s.opts.Store.Reader()).GetSecret(ctx, getParams(ref))
	if errors.Is(err, sql.ErrNoRows) {
		return Meta{}, nil
	}
	if err != nil {
		return Meta{}, fmt.Errorf("secrets: describe %s: %w", ref, err)
	}
	return Meta{IsSet: true, SetBy: row.SetBy, SetAt: time.UnixMilli(row.SetAt).UTC()}, nil
}

// Open implements Opener. It is not audited per call (it runs on every
// connection); it logs the reference at debug level.
func (s *Service) Open(ctx context.Context, ref Ref) (Value, error) {
	if err := ref.validate(); err != nil {
		return Value{}, err
	}
	row, err := secretsdb.New(s.opts.Store.Reader()).GetSecret(ctx, getParams(ref))
	if errors.Is(err, sql.ErrNoRows) {
		return Value{}, fmt.Errorf("%w: %s", ErrNotSet, ref)
	}
	if err != nil {
		return Value{}, fmt.Errorf("secrets: open %s: %w", ref, err)
	}
	pt, err := s.decrypt(s.key, row)
	if err != nil {
		return Value{}, fmt.Errorf("%w: %s", err, ref)
	}
	s.opts.Logger.DebugContext(ctx, "secret opened", slog.String("secret", ref.String()))
	return Value{b: pt}, nil
}

func (s *Service) decrypt(k cipherKey, row secretsdb.Secret) ([]byte, error) {
	if row.KeyVersion != k.version {
		return nil, ErrDecrypt
	}
	return k.open(row.Ciphertext, row.Nonce, secretAD(row.ID, Kind(row.Kind), row.OwnerType, row.OwnerID))
}

func getParams(ref Ref) secretsdb.GetSecretParams {
	return secretsdb.GetSecretParams{OwnerType: ref.OwnerType, OwnerID: ref.OwnerID, Kind: string(ref.Kind)}
}
