// Package audit is the append-only, hash-chained event log. Every state change
// in dbairlock records one event through Recorder.Record inside the same
// store transaction as the change, so either both commit or neither does.
//
// Each row stores the hash of the previous row and its own hash,
// SHA-256(prev_hash || canonical JSON of the row). Verifier walks the chain
// and reports the first edited, missing or reordered row.
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"

	"github.com/alvaroibarguen/dbairlock/internal/audit/auditdb"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/platform/ids"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Event is what a module records. The actor comes from the context and the
// id, time and hashes are filled in by the Recorder.
type Event struct {
	Action      Action
	SubjectType string
	SubjectID   string
	// Payload is marshaled to a JSON object (nil records {}). Strings longer
	// than MaxPayloadString are truncated. It must never contain a secret.
	Payload any
}

var actionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// Recorder appends events to the audit log.
type Recorder struct {
	clock clock.Clock
	ids   ids.Generator
}

// NewRecorder returns a Recorder that stamps events with c and g.
func NewRecorder(c clock.Clock, g ids.Generator) *Recorder {
	return &Recorder{clock: c, ids: g}
}

// Record appends ev inside the caller's transaction. It fails, and the caller
// must then fail its transaction, if ctx carries no Actor, the event is
// invalid, or the insert fails. Because the store has a single writer
// connection and Record reads the chain head inside tx, events are chained in
// commit order without further locking.
func (r *Recorder) Record(ctx context.Context, tx *store.Tx, ev Event) error {
	if tx == nil {
		return errors.New("audit: Record needs the caller's transaction")
	}
	actor, ok := ActorFrom(ctx)
	if !ok {
		return ErrNoActor
	}
	if err := actor.validate(); err != nil {
		return err
	}
	if !actionPattern.MatchString(string(ev.Action)) {
		return fmt.Errorf("audit: invalid action %q", ev.Action)
	}
	payload, err := preparePayload(ev.Payload)
	if err != nil {
		return err
	}

	repo := NewRepository(tx)
	lastSeq, prev, found, err := repo.Last(ctx)
	if err != nil {
		return fmt.Errorf("audit: read chain head: %w", err)
	}
	if !found {
		prev = make([]byte, hashSize)
	}
	// Continue from the highest seq ever assigned, not the newest surviving
	// row, so a deleted tail shows up as a gap instead of being papered over.
	high, err := repo.SequenceHigh(ctx)
	if err != nil {
		return fmt.Errorf("audit: read sequence: %w", err)
	}
	rec := record{
		Seq:         max(lastSeq, high) + 1,
		ID:          r.ids.NewID(),
		OccurredAt:  r.clock.Now().UnixMilli(),
		ActorUserID: actor.UserID,
		ActorEmail:  actor.Email,
		Via:         string(actor.Via),
		APITokenID:  actor.APITokenID,
		IP:          actor.IP,
		Action:      string(ev.Action),
		SubjectType: ev.SubjectType,
		SubjectID:   ev.SubjectID,
		Payload:     payload,
	}
	hash, err := chainHash(prev, rec)
	if err != nil {
		return err
	}
	err = repo.Insert(ctx, auditdb.InsertEventParams{
		Seq:         rec.Seq,
		ID:          rec.ID,
		OccurredAt:  rec.OccurredAt,
		ActorUserID: rec.ActorUserID,
		ActorEmail:  rec.ActorEmail,
		Via:         rec.Via,
		ApiTokenID:  rec.APITokenID,
		Ip:          rec.IP,
		Action:      rec.Action,
		SubjectType: rec.SubjectType,
		SubjectID:   rec.SubjectID,
		Payload:     rec.Payload,
		PrevHash:    prev,
		Hash:        hash,
	})
	if err != nil {
		return fmt.Errorf("audit: insert event: %w", err)
	}
	return nil
}

// preparePayload checks, marshals, truncates and canonicalizes a payload.
func preparePayload(p any) (string, error) {
	if err := checkPayload(p); err != nil {
		return "", err
	}
	if p == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("audit: marshal payload: %w", err)
	}
	obj, err := decodeObject(raw)
	if err != nil {
		return "", err
	}
	out, err := encodeCanonical(truncate(obj))
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// SecretMarker is implemented by types that hold a secret value and so must
// never appear in an audit payload. secrets.Value implements it; audit cannot
// import internal/secrets (depguard rule 5), so the marker lives here.
type SecretMarker interface {
	SecretMarker()
}

// ErrSecretInPayload means an event payload contained a secret value.
var ErrSecretInPayload = errors.New("audit: payload contains a secret value")

var secretMarkerType = reflect.TypeFor[SecretMarker]()

// maxPayloadDepth bounds the payload walk. Payloads are small, flat records;
// anything deeper is a mistake.
const maxPayloadDepth = 32

// checkPayload enforces payload hygiene before anything is serialized: it
// walks the payload (structs including unexported fields, maps, slices,
// arrays, pointers and interfaces) and rejects it if any value anywhere is a
// secret, naming the offending path.
func checkPayload(p any) error {
	if p == nil {
		return nil
	}
	return walkPayload(reflect.ValueOf(p), "payload", 0, map[uintptr]bool{})
}

func walkPayload(v reflect.Value, path string, depth int, seen map[uintptr]bool) error {
	if !v.IsValid() {
		return nil
	}
	if depth > maxPayloadDepth {
		return fmt.Errorf("audit: payload is nested deeper than %d levels at %s", maxPayloadDepth, path)
	}
	t := v.Type()
	if t.Implements(secretMarkerType) || reflect.PointerTo(t).Implements(secretMarkerType) {
		return fmt.Errorf("%w at %s", ErrSecretInPayload, path)
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || seen[v.Pointer()] {
			return nil
		}
		seen[v.Pointer()] = true
		return walkPayload(v.Elem(), path, depth+1, seen)
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return walkPayload(v.Elem(), path, depth+1, seen)
	case reflect.Struct:
		for i := range v.NumField() {
			if err := walkPayload(v.Field(i), path+"."+t.Field(i).Name, depth+1, seen); err != nil {
				return err
			}
		}
	case reflect.Map:
		iter := v.MapRange()
		for iter.Next() {
			key := fmt.Sprint(iter.Key())
			if err := walkPayload(iter.Key(), path+"["+key+"](key)", depth+1, seen); err != nil {
				return err
			}
			if err := walkPayload(iter.Value(), path+"["+key+"]", depth+1, seen); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil // bytes cannot hold a marker type
		}
		for i := range v.Len() {
			if err := walkPayload(v.Index(i), fmt.Sprintf("%s[%d]", path, i), depth+1, seen); err != nil {
				return err
			}
		}
	default:
	}
	return nil
}
