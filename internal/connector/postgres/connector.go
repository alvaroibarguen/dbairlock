// Package postgres is the PostgreSQL connector: validation with the real
// Postgres parser (pg_query_go), connections with pgx, schema
// introspection, the privilege check, the dry run and execution.
//
// It imports only the connector contract and platform packages. It is
// registered by internal/app; nothing here has init side effects.
package postgres

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// ID is the connector id.
const ID = "postgres"

// Supported major versions: every Postgres major version the PostgreSQL
// project still maintains, per docs/VERSIONS.md as of 2026-11-09 (14 to 18;
// 14 reaches end of life on 2026-11-12). Update together with that file.
const (
	minSupportedMajor = 14
	maxSupportedMajor = 18
)

// Config keys.
const (
	keyHost          = "host"
	keyPort          = "port"
	keyDatabase      = "database"
	keyDefaultSchema = "default_schema"
	keyUsername      = "username"
	keyPassword      = "password"
	keyTLSMode       = "tls_mode"
	keyCACert        = "ca_certificate"
)

// TLS modes.
const (
	tlsDisable    = "disable"
	tlsRequire    = "require"
	tlsVerifyFull = "verify-full"
)

// Pool and connect limits. Constants, not settings (docs/configuration.md):
// every target tolerates them, and dbairlock runs at most one change per
// target at a time plus occasional previews, so two connections suffice. A
// target that cannot accept a connection within 10 seconds counts as
// unreachable, and an execution is then deferred, never failed. Idle
// connections are closed after 5 minutes and every connection is renewed
// after 30, so a failover or a password change is picked up without
// holding sessions on the target.
const (
	defaultPort   = 5432
	defaultSchema = "public"
	poolMaxConns  = 2
	connectTimout = 10 * time.Second
	poolMaxIdle   = 5 * time.Minute
	poolMaxLife   = 30 * time.Minute
)

// Connector is the Postgres connector. The zero value is ready to use and
// stateless; all per-target state lives in the Conn returned by Open.
type Connector struct{}

// New returns the Postgres connector.
func New() *Connector { return &Connector{} }

var _ connector.Connector = (*Connector)(nil)

// Info describes the connector.
func (*Connector) Info() connector.Info {
	return connector.Info{ID: ID, DisplayName: "PostgreSQL", Dialect: "postgresql", DefaultPort: defaultPort}
}

// ConfigFields describes the connection form.
func (*Connector) ConfigFields() []connector.Field {
	return []connector.Field{
		{Key: keyHost, Label: "Host", Type: connector.FieldString, Required: true, Help: "Host name or IP address reachable from dbairlock."},
		{Key: keyPort, Label: "Port", Type: connector.FieldInt, Required: true, Default: defaultPort},
		{Key: keyDatabase, Label: "Database", Type: connector.FieldString, Required: true},
		{Key: keyDefaultSchema, Label: "Default schema", Type: connector.FieldString, Required: true, Default: defaultSchema,
			Help: "Unqualified table names always resolve to this schema, whatever the database user's own search_path is."},
		{Key: keyUsername, Label: "Username", Type: connector.FieldString, Required: true,
			Help: "A dedicated user with only SELECT, INSERT, UPDATE and DELETE on the application schema."},
		{Key: keyPassword, Label: "Password", Type: connector.FieldSecret, Required: true},
		{Key: keyTLSMode, Label: "TLS", Type: connector.FieldEnum, Required: true, Default: tlsVerifyFull,
			Options: []string{tlsDisable, tlsRequire, tlsVerifyFull},
			Help:    "verify-full checks the server certificate and host name; require encrypts without verifying."},
		{Key: keyCACert, Label: "CA certificate", Type: connector.FieldText,
			Help: "PEM certificate of the CA that signed the server certificate. Leave empty to use the system roots."},
	}
}

var identRe = regexp.MustCompile(`^[^\x00]{1,63}$`)

// ValidateConfig checks and normalizes the connection fields.
func (c *Connector) ValidateConfig(cfg connector.Config) (connector.Config, error) {
	known := map[string]bool{}
	for _, f := range c.ConfigFields() {
		known[f.Key] = true
	}
	for k := range cfg {
		if k == keyPassword {
			return nil, errors.New("the password is a secret and must not be part of the connection config")
		}
		if !known[k] {
			return nil, fmt.Errorf("unknown field %q", k)
		}
	}
	out := connector.Config{}
	str := func(key string, def string, required bool) (string, error) {
		v, ok := cfg[key]
		if !ok || v == nil {
			v = def
		}
		s, isStr := v.(string)
		if !isStr {
			return "", fmt.Errorf("%s must be a string", key)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			s = def
		}
		if required && s == "" {
			return "", fmt.Errorf("%s is required", key)
		}
		return s, nil
	}
	var err error
	host, err := str(keyHost, "", true)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(host, " /\\\x00") || strings.HasPrefix(host, "-") {
		return nil, fmt.Errorf("host %q is not a valid host name or address", host)
	}
	if err := connector.CheckHost(strings.Trim(host, "[]")); err != nil {
		return nil, err
	}
	out[keyHost] = host

	port, err := intField(cfg[keyPort], defaultPort)
	if err != nil {
		return nil, fmt.Errorf("port: %w", err)
	}
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("port %d is out of range", port)
	}
	out[keyPort] = port

	for _, k := range []string{keyDatabase, keyUsername} {
		s, err := str(k, "", true)
		if err != nil {
			return nil, err
		}
		if !identRe.MatchString(s) {
			return nil, fmt.Errorf("%s must be 1 to 63 characters", k)
		}
		out[k] = s
	}
	schema, err := str(keyDefaultSchema, defaultSchema, true)
	if err != nil {
		return nil, err
	}
	if !identRe.MatchString(schema) {
		return nil, fmt.Errorf("default schema must be 1 to 63 characters")
	}
	if l := strings.ToLower(schema); strings.HasPrefix(l, "pg_") || l == "information_schema" {
		return nil, fmt.Errorf("default schema %q is a system schema", schema)
	}
	out[keyDefaultSchema] = schema

	mode, err := str(keyTLSMode, tlsVerifyFull, true)
	if err != nil {
		return nil, err
	}
	switch mode {
	case tlsDisable, tlsRequire, tlsVerifyFull:
	default:
		return nil, fmt.Errorf("tls_mode must be one of disable, require, verify-full")
	}
	out[keyTLSMode] = mode

	ca, err := str(keyCACert, "", false)
	if err != nil {
		return nil, err
	}
	if ca != "" {
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("the CA certificate is not valid PEM")
		}
		out[keyCACert] = ca
	}
	return out, nil
}

func intField(v any, def int) (int, error) {
	switch x := v.(type) {
	case nil:
		return def, nil
	case int:
		return x, nil
	case int32:
		return int(x), nil
	case int64:
		return int(x), nil
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("%v is not a whole number", x)
		}
		return int(x), nil
	case string:
		if strings.TrimSpace(x) == "" {
			return def, nil
		}
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("%q is not a number", x)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}

// Validate checks one statement (PLAN.md 5.1). Unqualified tables resolve to
// the connection's default schema.
func (*Connector) Validate(cfg connector.Config, sql string, p connector.Policy) (connector.Statement, error) {
	schema := cfg.String(keyDefaultSchema)
	if schema == "" {
		schema = defaultSchema
	}
	return validate(sql, schema, p)
}

// Open creates a small connection pool for one target. It does not connect
// eagerly.
func (c *Connector) Open(_ context.Context, cfg connector.Config, password []byte) (connector.Conn, error) {
	cfg, err := c.ValidateConfig(cfg)
	if err != nil {
		return nil, err
	}
	pc, err := poolConfig(cfg, password)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), pc)
	if err != nil {
		return nil, fmt.Errorf("postgres: create pool: %w", err)
	}
	return &conn{pool: pool, schema: cfg.String(keyDefaultSchema)}, nil
}

// poolConfig builds the pgx pool configuration. Every connection setting is
// set explicitly here, overriding anything pgx would take from libpq
// environment defaults.
func poolConfig(cfg connector.Config, password []byte) (*pgxpool.Config, error) {
	pc, err := pgxpool.ParseConfig("")
	if err != nil {
		return nil, fmt.Errorf("postgres: base config: %w", err)
	}
	host := strings.Trim(cfg.String(keyHost), "[]")
	cc := pc.ConnConfig
	cc.Host = host
	cc.Port = uint16(cfg.Int(keyPort)) // #nosec G115 -- validated to 1..65535
	cc.Database = cfg.String(keyDatabase)
	cc.User = cfg.String(keyUsername)
	cc.Password = string(password)
	cc.ConnectTimeout = connectTimout
	cc.Fallbacks = nil
	cc.KerberosSrvName, cc.KerberosSpn = "", ""
	cc.SSLNegotiation = ""
	cc.RuntimeParams = map[string]string{"application_name": "dbairlock"}
	cc.DefaultQueryExecMode = pgx.QueryExecModeExec // text results, no cached prepared statements
	cc.DialFunc = connector.SafeDialContext(&net.Dialer{KeepAlive: 30 * time.Second})
	cc.LookupFunc = func(_ context.Context, h string) ([]string, error) { return []string{h}, nil } // resolved in DialFunc
	tlsCfg, err := tlsConfig(cfg, host)
	if err != nil {
		return nil, err
	}
	cc.TLSConfig = tlsCfg
	pc.MaxConns = poolMaxConns
	pc.MinConns = 0
	pc.MaxConnIdleTime = poolMaxIdle
	pc.MaxConnLifetime = poolMaxLife
	return pc, nil
}

func tlsConfig(cfg connector.Config, host string) (*tls.Config, error) {
	mode := cfg.String(keyTLSMode)
	if mode == tlsDisable {
		return nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
	if ca := cfg.String(keyCACert); ca != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, errors.New("the CA certificate is not valid PEM")
		}
		tc.RootCAs = pool
	}
	if mode == tlsRequire {
		// Encrypt without verifying, as libpq's sslmode=require.
		tc.InsecureSkipVerify = true // #nosec G402 -- the admin chose "require" over "verify-full"
	}
	return tc, nil
}
