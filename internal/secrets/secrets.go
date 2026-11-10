// Package secrets is the write-only secret store. Secrets (target database
// passwords, the Google client secret, webhook URLs) are encrypted with
// AES-256-GCM under the master key in <data-dir>/master.key.
//
// "Set only, no read" is enforced in three places:
//   - the interface split: transports hold a Writer, which can set, clear and
//     describe a secret but has no way to read it; only targets, identity and
//     notify are given an Opener (depguard and internal/archtest);
//   - the API contract: secret fields are write-only in api/openapi.yaml and a
//     contract test rejects them in any response schema;
//   - the Value type, whose every representation is "[redacted]".
package secrets

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Kind is what a secret is for. Values match the CHECK constraint on
// secrets.kind.
type Kind string

// Kinds.
const (
	KindTargetPassword    Kind = "target_password"
	KindOAuthClientSecret Kind = "oauth_client_secret" //nolint:gosec // a kind name, not a credential
	KindWebhookURL        Kind = "webhook_url"
)

func (k Kind) valid() bool {
	switch k {
	case KindTargetPassword, KindOAuthClientSecret, KindWebhookURL:
		return true
	}
	return false
}

// MaxValueBytes bounds a secret value. Passwords, client secrets and webhook
// URLs are all far shorter.
const MaxValueBytes = 4096

// Ref names a secret: what it is for and which record owns it, for example
// {KindTargetPassword, "connection", "<connection id>"}.
type Ref struct {
	Kind      Kind   `json:"kind"`
	OwnerType string `json:"owner_type"`
	OwnerID   string `json:"owner_id"`
}

func (r Ref) validate() error {
	if !r.Kind.valid() {
		return fmt.Errorf("secrets: invalid kind %q", r.Kind)
	}
	if r.OwnerType == "" || r.OwnerID == "" {
		return errors.New("secrets: a reference needs an owner type and id")
	}
	return nil
}

// String formats the reference (never the value) for logs and errors.
func (r Ref) String() string { return string(r.Kind) + ":" + r.OwnerType + "/" + r.OwnerID }

// Meta describes a secret without revealing anything about its value: no
// length, hash, prefix or fingerprint.
type Meta struct {
	IsSet bool
	SetBy string
	SetAt time.Time
}

// Writer can set, replace, clear and describe secrets, but never read them.
// This is all a transport (httpapi, mcpserver) or setup may hold.
type Writer interface {
	// Set creates or replaces the secret inside the caller's transaction and
	// records secret.set or secret.replaced.
	Set(ctx context.Context, tx *store.Tx, ref Ref, plaintext Value) error
	// Clear removes the secret, if set, and records secret.cleared.
	Clear(ctx context.Context, tx *store.Tx, ref Ref) error
	// Describe reports whether the secret is set, by whom and when.
	Describe(ctx context.Context, ref Ref) (Meta, error)
}

// Opener decrypts a secret at the moment of use. Only targets, identity and
// notify may hold one (PLAN 4.2 rule 5).
type Opener interface {
	Open(ctx context.Context, ref Ref) (Value, error)
}

var (
	// ErrNotSet means the referenced secret does not exist.
	ErrNotSet = errors.New("secrets: secret is not set")
	// ErrDecrypt means a stored secret could not be decrypted: the ciphertext
	// was altered or moved to another row, or the key is wrong.
	ErrDecrypt = errors.New("secrets: secret could not be decrypted")
	// ErrInvalidValue means a value is empty or too long.
	ErrInvalidValue = errors.New("secrets: invalid value")
	// ErrKeyMismatch means master.key does not belong to this database.
	ErrKeyMismatch = errors.New("secrets: master.key does not match this database")
	// ErrKeyFile means master.key is unusable: wrong size or permissions.
	ErrKeyFile = errors.New("secrets: master.key is unusable")
)
