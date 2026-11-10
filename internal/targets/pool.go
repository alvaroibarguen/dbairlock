package targets

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
	"github.com/alvaroibarguen/dbairlock/internal/secrets"
)

// pooled is one cached connector connection (a small pool) for one
// connection id. key is the connection's UpdatedAt when it was opened: a
// record with another key means the config or password changed, and the
// pool is replaced.
type pooled struct {
	conn  connector.Conn
	key   int64
	refs  int
	stale bool // removed from the cache; closed when the last user releases it
}

// ErrNoPassword means a connection has no password in the secret store.
var ErrNoPassword = errors.New("targets: the connection has no password; set one")

// WithConn runs fn with an open connector connection for connection id. It
// holds the connection's run mutex for the duration, so at most one dry run
// or execution runs per target at a time, while other targets are not
// affected. It opens the password at the moment of use, reuses the cached
// pool when the connection has not changed, and always releases it.
//
// ctx bounds the wait for the run mutex as well as fn; callers set a
// deadline slightly longer than the statement timeout so a hung network
// connection cannot hold the mutex forever.
func (s *Service) WithConn(ctx context.Context, id string, fn func(ctx context.Context, c Connection, conn connector.Conn) error) error {
	unlock, err := s.lockRun(ctx, id)
	if err != nil {
		return err
	}
	defer unlock()
	c, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	p, err := s.acquire(ctx, c)
	if err != nil {
		return err
	}
	defer s.release(p)
	return fn(ctx, c, p.conn)
}

// lockRun takes the run mutex of connection id, or gives up when ctx ends.
func (s *Service) lockRun(ctx context.Context, id string) (func(), error) {
	s.mu.Lock()
	ch := s.locks[id]
	if ch == nil {
		ch = make(chan struct{}, 1)
		s.locks[id] = ch
	}
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("targets: waiting for another run on this connection: %w", ctx.Err())
	}
}

// acquire returns the cached pool for c, opening one if needed. Every
// acquire must be paired with release.
func (s *Service) acquire(ctx context.Context, c Connection) (*pooled, error) {
	if c.ArchivedAt != nil {
		return nil, ErrArchived
	}
	key := c.UpdatedAt.UnixMilli()
	var retired []*pooled
	defer func() { s.closeAll(retired) }()

	s.mu.Lock()
	if p := s.pools[c.ID]; p != nil {
		if p.key == key {
			p.refs++
			s.mu.Unlock()
			return p, nil
		}
		if p.key < key {
			retired = append(retired, s.retireLocked(c.ID, p)...)
		}
	}
	s.mu.Unlock()

	conn, err := s.open(ctx, c)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.pools[c.ID]; p != nil {
		switch {
		case p.key == key: // opened concurrently: use that one
			p.refs++
			retired = append(retired, &pooled{conn: conn, stale: true})
			return p, nil
		case p.key > key: // c is older than the cached record: use ours once, uncached
			return &pooled{conn: conn, key: key, refs: 1, stale: true}, nil
		default:
			retired = append(retired, s.retireLocked(c.ID, p)...)
		}
	}
	p := &pooled{conn: conn, key: key, refs: 1}
	s.pools[c.ID] = p
	return p, nil
}

// open decrypts the password and opens a connector connection for c.
func (s *Service) open(ctx context.Context, c Connection) (connector.Conn, error) {
	cn, ok := s.connectorFor(c.Connector)
	if !ok {
		return nil, fmt.Errorf("targets: connector %q is not registered", c.Connector)
	}
	pw, err := s.opts.Secrets.Open(ctx, passwordRef(c.ID))
	if errors.Is(err, secrets.ErrNotSet) {
		return nil, ErrNoPassword
	}
	if err != nil {
		return nil, err
	}
	return openWith(ctx, cn, c.Config, pw)
}

// openWith opens a connection with the password revealed only for the call.
func openWith(ctx context.Context, cn connector.Connector, cfg connector.Config, pw secrets.Value) (connector.Conn, error) {
	b := pw.Reveal()
	defer clear(b)
	conn, err := cn.Open(ctx, cloneConfig(cfg), b)
	if err != nil {
		return nil, fmt.Errorf("targets: open connection: %w", err)
	}
	return conn, nil
}

// release gives a pool back; a retired pool closes with its last user.
func (s *Service) release(p *pooled) {
	s.mu.Lock()
	p.refs--
	closeNow := p.stale && p.refs == 0
	s.mu.Unlock()
	if closeNow {
		s.closeAll([]*pooled{p})
	}
}

// retireLocked removes p from the cache. It returns p if nobody uses it, for
// the caller to close after unlocking. s.mu must be held.
func (s *Service) retireLocked(id string, p *pooled) []*pooled {
	if s.pools[id] == p {
		delete(s.pools, id)
	}
	p.stale = true
	if p.refs == 0 {
		return []*pooled{p}
	}
	return nil
}

func (s *Service) closeAll(ps []*pooled) {
	for _, p := range ps {
		if err := p.conn.Close(); err != nil {
			s.opts.Logger.Warn("closing a connection pool failed", slog.Any("error", err))
		}
	}
}

// invalidate drops the cached pool and schema of one connection. A pool in
// use closes when its user releases it.
func (s *Service) invalidate(id string) {
	s.mu.Lock()
	var retired []*pooled
	if p := s.pools[id]; p != nil {
		retired = s.retireLocked(id, p)
	}
	s.mu.Unlock()
	s.closeAll(retired)
	s.schemaMu.Lock()
	delete(s.schemas, id)
	s.schemaMu.Unlock()
}

// Close closes every cached pool. Call it once the server has stopped.
func (s *Service) Close() {
	s.mu.Lock()
	var retired []*pooled
	for id, p := range s.pools {
		retired = append(retired, s.retireLocked(id, p)...)
	}
	s.mu.Unlock()
	s.closeAll(retired)
}
