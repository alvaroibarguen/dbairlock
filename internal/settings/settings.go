// Package settings holds the typed instance settings. Every setting is
// declared in the registry (registry.go) with its type, default and
// validator; unknown keys are rejected. Readers use Get, which reads an
// in-memory snapshot. Update validates all changes together, writes them and
// their setting.changed audit events in one transaction, then swaps the
// snapshot and notifies listeners.
package settings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/alvaroibarguen/dbairlock/internal/audit"
	"github.com/alvaroibarguen/dbairlock/internal/platform/clock"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
	"github.com/alvaroibarguen/dbairlock/internal/settings/settingsdb"
	"github.com/alvaroibarguen/dbairlock/internal/store"
)

// Transactor is the store surface settings need.
type Transactor interface {
	Tx(ctx context.Context, fn func(tx *store.Tx) error) error
	Reader() store.DBTX
}

// Auditor records audit events inside the caller's transaction.
type Auditor interface {
	Record(ctx context.Context, tx *store.Tx, ev audit.Event) error
}

// SecretWriter is the part of the secret store settings use. It cannot read
// a secret back.
type SecretWriter interface {
	Set(ctx context.Context, tx *store.Tx, ref secrets.Ref, plaintext secrets.Value) error
	Clear(ctx context.Context, tx *store.Tx, ref secrets.Ref) error
	Describe(ctx context.Context, ref secrets.Ref) (secrets.Meta, error)
}

// Options configure the settings service.
type Options struct {
	Store   Transactor
	Audit   Auditor
	Secrets SecretWriter
	Clock   clock.Clock
	Logger  *slog.Logger
}

// Listener is called after a committed update with the names of the settings
// and secret slots it changed.
type Listener func(ctx context.Context, changed []string)

// Service is the settings module.
type Service struct {
	opts      Options
	mu        sync.Mutex // serializes Update so snapshots swap in commit order
	snap      atomic.Pointer[map[string]any]
	listeners []Listener
	lmu       sync.Mutex
}

// New loads the stored settings over the registered defaults. A stored value
// for an unknown key, or one that no longer validates, is ignored with a
// warning and the default applies.
func New(ctx context.Context, opts Options) (*Service, error) {
	if opts.Store == nil || opts.Audit == nil || opts.Secrets == nil || opts.Clock == nil || opts.Logger == nil {
		return nil, errors.New("settings: store, audit, secrets, clock and logger are required")
	}
	values := make(map[string]any, len(definitions))
	for _, d := range definitions {
		values[d.Key] = cloneValue(d.Default)
	}
	rows, err := settingsdb.New(opts.Store.Reader()).ListSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("settings: load: %w", err)
	}
	for _, row := range rows {
		d, ok := definition(row.Key)
		if !ok {
			opts.Logger.Warn("ignoring a stored setting this version does not know", slog.String("key", row.Key))
			continue
		}
		v, err := d.parse(json.RawMessage(row.Value))
		if err != nil {
			opts.Logger.Warn("ignoring a stored setting that is no longer valid; using the default",
				slog.String("key", row.Key), slog.String("reason", err.Error()))
			continue
		}
		values[row.Key] = v
	}
	s := &Service{opts: opts}
	s.snap.Store(&values)
	return s, nil
}

// Source is what Get reads from. *Service implements it; modules that read
// settings declare it (or embed it) in their own dependency interfaces.
type Source interface {
	Value(key string) any
}

// Value returns the current value of a registered setting, or nil for an
// unknown key. Prefer the typed Get.
func (s *Service) Value(key string) any {
	return cloneValue((*s.snap.Load())[key])
}

// Get returns the current value of k from the in-memory snapshot. It panics
// if the registry declares k with a different type, which the registry tests
// rule out.
func Get[T any](src Source, k Key[T]) T {
	v, ok := src.Value(k.name).(T)
	if !ok {
		panic(fmt.Sprintf("settings: %s is not registered as %T", k.name, *new(T)))
	}
	return v
}

// Values returns a copy of every setting's current value, keyed by name.
func (s *Service) Values() map[string]any {
	cur := *s.snap.Load()
	out := make(map[string]any, len(cur))
	for k, v := range cur {
		out[k] = cloneValue(v)
	}
	return out
}

// PublicValues returns the settings that may be shown before sign-in.
func (s *Service) PublicValues() map[string]any {
	out := map[string]any{}
	for _, d := range definitions {
		if d.Public {
			out[d.Key] = s.Value(d.Key)
		}
	}
	return out
}

// SecretStatus describes each settings-related secret, keyed by slot name.
func (s *Service) SecretStatus(ctx context.Context) (map[string]secrets.Meta, error) {
	out := make(map[string]secrets.Meta, len(secretSlots))
	for _, slot := range secretSlots {
		m, err := s.opts.Secrets.Describe(ctx, slot.Ref)
		if err != nil {
			return nil, err
		}
		out[slot.Name] = m
	}
	return out, nil
}

// OnChange registers fn to run after every committed update.
func (s *Service) OnChange(fn Listener) {
	s.lmu.Lock()
	defer s.lmu.Unlock()
	s.listeners = append(s.listeners, fn)
}

// SecretChange replaces (Value) or clears (Clear) a settings-related secret.
type SecretChange struct {
	Clear bool
	Value secrets.Value
}

// Changes is one atomic settings update. Values maps a setting key to its new
// value (a json.RawMessage or a Go value of the registered type).
type Changes struct {
	Values  map[string]any
	Secrets map[string]SecretChange
}

// FieldError is a validation failure on one setting or secret slot.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError lists every invalid field of a rejected update. Nothing was
// changed.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) Error() string {
	names := make([]string, len(e.Fields))
	for i, f := range e.Fields {
		names[i] = f.Field
	}
	return "settings: invalid " + strings.Join(names, ", ")
}

// ErrInvalid matches every *ValidationError with errors.Is.
var ErrInvalid = errors.New("settings: invalid update")

// Is reports whether target is ErrInvalid.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// Update validates every change together and applies all of them or none. In
// one transaction it writes each changed setting with a setting.changed audit
// event carrying the old and new values, and sets or clears each secret
// (which records its own event, without the value). Then it swaps the
// snapshot, so the change is visible to readers immediately, and notifies
// listeners. Setting a value equal to the current one records nothing.
func (s *Service) Update(ctx context.Context, ch Changes) error {
	parsed, secretChanges, err := validate(ch)
	if err != nil {
		return err
	}
	actor, ok := audit.ActorFrom(ctx)
	if !ok {
		return audit.ErrNoActor
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	cur := *s.snap.Load()
	next := maps.Clone(cur)
	var changed []string

	err = s.opts.Store.Tx(ctx, func(tx *store.Tx) error {
		q := settingsdb.New(tx)
		now := s.opts.Clock.Now().UnixMilli()
		for _, key := range slices.Sorted(maps.Keys(parsed)) {
			v := parsed[key]
			old := cur[key]
			if reflect.DeepEqual(old, v) {
				continue
			}
			raw, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("settings: encode %s: %w", key, err)
			}
			if err := q.PutSetting(ctx, settingsdb.PutSettingParams{
				Key: key, Value: string(raw), UpdatedBy: actor.Label(), UpdatedAt: now,
			}); err != nil {
				return fmt.Errorf("settings: write %s: %w", key, err)
			}
			if err := s.opts.Audit.Record(ctx, tx, audit.Event{
				Action:      audit.SettingChanged,
				SubjectType: "setting",
				SubjectID:   key,
				Payload:     map[string]any{"key": key, "old": old, "new": v},
			}); err != nil {
				return err
			}
			next[key] = v
			changed = append(changed, key)
		}
		for _, name := range slices.Sorted(maps.Keys(secretChanges)) {
			sc := secretChanges[name]
			slot, _ := secretSlot(name)
			var err error
			if sc.Clear {
				err = s.opts.Secrets.Clear(ctx, tx, slot.Ref)
			} else {
				err = s.opts.Secrets.Set(ctx, tx, slot.Ref, sc.Value)
			}
			if err != nil {
				return fmt.Errorf("settings: %s: %w", name, err)
			}
			changed = append(changed, name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.snap.Store(&next)

	if len(changed) > 0 {
		s.lmu.Lock()
		ls := slices.Clone(s.listeners)
		s.lmu.Unlock()
		for _, fn := range ls {
			fn(ctx, changed)
		}
	}
	return nil
}

// validate checks every change and returns the normalized values, or a
// *ValidationError listing every invalid field.
func validate(ch Changes) (map[string]any, map[string]SecretChange, error) {
	var fields []FieldError
	parsed := make(map[string]any, len(ch.Values))
	for key, raw := range ch.Values {
		d, ok := definition(key)
		if !ok {
			fields = append(fields, FieldError{Field: key, Message: "Unknown setting."})
			continue
		}
		v, err := d.parse(raw)
		if err != nil {
			var msg invalid
			if !errors.As(err, &msg) {
				return nil, nil, err
			}
			fields = append(fields, FieldError{Field: key, Message: string(msg)})
			continue
		}
		parsed[key] = v
	}
	for name, sc := range ch.Secrets {
		if _, ok := secretSlot(name); !ok {
			fields = append(fields, FieldError{Field: name, Message: "Unknown secret."})
			continue
		}
		if sc.Clear {
			continue
		}
		switch {
		case sc.Value.IsEmpty():
			fields = append(fields, FieldError{Field: name, Message: "Enter a value, or clear the secret instead."})
		case sc.Value.Len() > secrets.MaxValueBytes:
			fields = append(fields, FieldError{Field: name, Message: fmt.Sprintf("Must be at most %d bytes.", secrets.MaxValueBytes)})
		}
	}
	if len(fields) > 0 {
		sort.Slice(fields, func(i, j int) bool { return fields[i].Field < fields[j].Field })
		return nil, nil, &ValidationError{Fields: fields}
	}
	return parsed, ch.Secrets, nil
}

func cloneValue(v any) any {
	if l, ok := v.([]string); ok {
		return slices.Clone(l)
	}
	return v
}
