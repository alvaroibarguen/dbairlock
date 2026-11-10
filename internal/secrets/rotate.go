package secrets

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/secrets/secretsdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// RotateResult reports a completed rotation.
type RotateResult struct {
	Secrets    int
	KeyVersion int64
}

// rotateHook, when set by a test, runs at named stages of Rotate and can
// return an error to simulate a crash there.
var rotateHook func(stage string) error

func rotateStage(stage string) error {
	if rotateHook == nil {
		return nil
	}
	return rotateHook(stage)
}

// Rotate replaces the master key. ctx must carry the audit actor (the CLI
// uses audit.ViaCLI). The server must be stopped: a running server keeps the
// old key in memory.
//
// The steps are ordered so that a crash at any point leaves a usable key:
//
//  1. Load and verify the current key (finishing any earlier interrupted
//     rotation).
//  2. Write the new key to master.key.new and the current key to
//     master.key.old, each atomically.
//  3. In one transaction, re-encrypt every secret and the key check under the
//     new key with key_version+1, and record master_key.rotated.
//  4. Rename master.key.new over master.key.
//
// A crash before step 3 commits leaves the database on the old key, which is
// still master.key; the next start discards master.key.new. A crash after
// the commit but before step 4 leaves the database on the new key; the next
// start finds it in master.key.new and finishes the rename. master.key.old is
// removed by the next successful start.
func Rotate(ctx context.Context, opts Options) (RotateResult, error) {
	if err := opts.validate(); err != nil {
		return RotateResult{}, err
	}
	if _, ok := audit.ActorFrom(ctx); !ok {
		return RotateResult{}, audit.ErrNoActor
	}
	oldKey, err := loadKey(ctx, opts)
	if err != nil {
		return RotateResult{}, err
	}
	path := keyPath(opts.DataDir)
	oldRaw, err := readKeyFile(path)
	if err != nil {
		return RotateResult{}, err
	}
	newRaw, err := newRandomKey()
	if err != nil {
		return RotateResult{}, err
	}
	newKey, err := newCipherKey(newRaw, oldKey.version+1)
	if err != nil {
		return RotateResult{}, err
	}

	if err := writeKeyFileAtomic(path+newKeySuffix, newRaw); err != nil {
		return RotateResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = removeIfExists(path + newKeySuffix)
		}
	}()
	if err := writeKeyFileAtomic(path+oldKeySuffix, oldRaw); err != nil {
		return RotateResult{}, err
	}
	if err := rotateStage("before-commit"); err != nil {
		return RotateResult{}, err
	}

	svc := &Service{opts: opts, key: oldKey}
	var count int
	err = opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := secretsdb.New(tx)
		rows, err := q.ListSecrets(ctx)
		if err != nil {
			return fmt.Errorf("secrets: list: %w", err)
		}
		for _, row := range rows {
			pt, err := svc.decrypt(oldKey, row)
			if err != nil {
				return fmt.Errorf("secrets: rotation aborted, secret %s cannot be decrypted: %w", row.ID, err)
			}
			ct, nonce, err := newKey.seal(pt, secretAD(row.ID, Kind(row.Kind), row.OwnerType, row.OwnerID))
			if err != nil {
				return err
			}
			if err := q.ReencryptSecret(ctx, secretsdb.ReencryptSecretParams{
				Ciphertext: ct, Nonce: nonce, KeyVersion: newKey.version, ID: row.ID,
			}); err != nil {
				return fmt.Errorf("secrets: re-encrypt %s: %w", row.ID, err)
			}
		}
		count = len(rows)
		ct, nonce, err := newKey.seal([]byte(keyCheckPlaintext), keyCheckAD())
		if err != nil {
			return err
		}
		if err := q.PutKeyCheck(ctx, secretsdb.PutKeyCheckParams{
			Ciphertext: ct, Nonce: nonce, KeyVersion: newKey.version, UpdatedAt: opts.Clock.Now().UnixMilli(),
		}); err != nil {
			return fmt.Errorf("secrets: store key check: %w", err)
		}
		return opts.Audit.Record(ctx, tx, audit.Event{
			Action:      audit.MasterKeyRotated,
			SubjectType: "master_key",
			Payload:     map[string]any{"key_version": newKey.version, "secrets": count},
		})
	})
	if err != nil {
		return RotateResult{}, err
	}
	committed = true

	if err := rotateStage("after-commit"); err != nil {
		return RotateResult{}, err
	}
	if err := renameKeyFile(path+newKeySuffix, path); err != nil {
		return RotateResult{}, fmt.Errorf("%w (the database already uses the new key; the next start installs master.key.new)", err)
	}
	opts.Logger.Info("master key rotated", slog.Int64("key_version", newKey.version), slog.Int("secrets", count))
	return RotateResult{Secrets: count, KeyVersion: newKey.version}, nil
}
