package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Tx is a write transaction on the single writer connection. It exists only
// inside the function passed to Store.Tx, so code that receives a *Tx is
// guaranteed to be part of a transaction it does not control: it can neither
// commit nor roll back. Pass it to sqlc packages (it implements DBTX) and to
// audit.Recorder.Record so the audit row commits with the state change.
type Tx struct {
	tx *sql.Tx
}

// ExecContext implements DBTX.
func (t *Tx) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, q, args...)
}

// PrepareContext implements DBTX.
func (t *Tx) PrepareContext(ctx context.Context, q string) (*sql.Stmt, error) {
	return t.tx.PrepareContext(ctx, q)
}

// QueryContext implements DBTX.
func (t *Tx) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, q, args...)
}

// QueryRowContext implements DBTX.
func (t *Tx) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, q, args...)
}

// Tx runs fn in a write transaction that begins with BEGIN IMMEDIATE on the
// writer connection. It commits if fn returns nil and rolls back if fn returns
// an error or panics (the panic is re-raised after the rollback). It is the
// only way modules write.
func (s *Store) Tx(ctx context.Context, fn func(tx *Tx) error) (err error) {
	if !s.ready.Load() {
		return ErrNotReady
	}
	sqlTx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin: %w", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rbErr := sqlTx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) && err != nil {
			err = errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
	}()

	if err := fn(&Tx{tx: sqlTx}); err != nil {
		return err
	}
	if err := sqlTx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", err)
	}
	committed = true
	return nil
}
