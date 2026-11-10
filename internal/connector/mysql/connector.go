// Package mysql is the MySQL connector: validation with the TiDB parser
// (github.com/pingcap/tidb/pkg/parser), connections with go-sql-driver/mysql,
// schema introspection, the privilege check, the dry run and execution.
//
// It imports only the connector contract and platform packages. It is
// registered by internal/app; nothing here has init side effects (the TiDB
// parser's test_driver package registers its literal constructors in its
// own init, which is the documented way to use the parser on its own).
//
// # The TiDB parser is not MySQL's parser
//
// It accepts nearly all MySQL DML, but it is a different implementation.
// Because unknown constructs are rejected, a gap costs convenience, not
// safety. The connector closes the gaps that could change a statement's
// meaning between validation and execution:
//
//   - Executable comments (/*! */, /*+ */, MariaDB /*M! */ and TiDB's
//     /*T! */) are rejected before parsing, wherever they appear.
//   - Every transaction pins the lexical parts of sql_mode to what the
//     parser assumes: ANSI_QUOTES, NO_BACKSLASH_ESCAPES, PIPES_AS_CONCAT,
//     HIGH_NOT_PRECEDENCE and IGNORE_SPACE are removed from the session's
//     sql_mode. The other modes (strictness, zero dates and so on) are kept
//     as the server has them, because they change how values are stored and
//     the application depends on them.
//
// Known gaps (statements MySQL accepts that are rejected here):
//
//   - Index hints (USE INDEX, FORCE INDEX), partition selection and
//     optimizer hints.
//   - INSERT ... SET, INSERT ... SELECT, INSERT ... TABLE and VALUES ROW()
//     forms other than a plain VALUES list.
//   - Single-table DELETE or UPDATE with ORDER BY or LIMIT.
//   - Derived tables and set operations (UNION) inside subqueries.
//   - Syntax newer than the parser version in go.mod.
//
// Known limitations:
//
//   - A dry run of an INSERT does not show the inserted rows: MySQL has no
//     RETURNING clause, so only the row count is reported.
//   - A dry run consumes AUTO_INCREMENT values and fires triggers before it
//     rolls back. Triggers with effects outside the transaction are not
//     undone.
//   - A zero date (0000-00-00) and the date 0001-01-01 00:00:00 have the
//     same fingerprint encoding.
package mysql

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-sql-driver/mysql"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// ID is the connector id.
const ID = "mysql"

// Supported versions: every MySQL LTS release Oracle still maintains, per
// docs/VERSIONS.md as of 2026-11-09 (8.4 LTS and 9.7 LTS). Innovation
// releases are not supported targets. Update together with that file.
var supportedReleases = []struct{ major, minor int }{
	{8, 4},
	{9, 7},
}

// Config keys.
const (
	keyHost     = "host"
	keyPort     = "port"
	keyDatabase = "database"
	keyUsername = "username"
	keyPassword = "password"
	keyTLSMode  = "tls_mode"
	keyCACert   = "ca_certificate"
)

// TLS modes, named as in MySQL's --ssl-mode.
const (
	tlsDisabled       = "disabled"
	tlsRequired       = "required"
	tlsVerifyIdentity = "verify-identity"
)

const (
	defaultPort    = 3306
	poolMaxConns   = 2
	connectTimeout = 10 * time.Second
	maxDatabaseLen = 64
	maxUsernameLen = 32
	programName    = "dbairlock"
	// labelAttr is the connection attribute that carries the RunInfo label.
	// It is visible in performance_schema.session_connect_attrs.
	labelAttr = "dbairlock_run"
)

// Connector is the MySQL connector. The zero value is ready to use and
// stateless; all per-target state lives in the Conn returned by Open.
type Connector struct{}

// New returns the MySQL connector.
func New() *Connector { return &Connector{} }

var _ connector.Connector = (*Connector)(nil)

// Info describes the connector.
func (*Connector) Info() connector.Info {
	return connector.Info{ID: ID, DisplayName: "MySQL", Dialect: "mysql", DefaultPort: defaultPort}
}

// ConfigFields describes the connection form.
func (*Connector) ConfigFields() []connector.Field {
	return []connector.Field{
		{Key: keyHost, Label: "Host", Type: connector.FieldString, Required: true, Help: "Host name or IP address reachable from dbairlock."},
		{Key: keyPort, Label: "Port", Type: connector.FieldInt, Required: true, Default: defaultPort},
		{Key: keyDatabase, Label: "Database", Type: connector.FieldString, Required: true,
			Help: "Statements may change tables in this database only. Tables qualified with another database are rejected."},
		{Key: keyUsername, Label: "Username", Type: connector.FieldString, Required: true,
			Help: "A dedicated user with only SELECT, INSERT, UPDATE and DELETE on this database."},
		{Key: keyPassword, Label: "Password", Type: connector.FieldSecret, Required: true},
		{Key: keyTLSMode, Label: "TLS", Type: connector.FieldEnum, Required: true, Default: tlsVerifyIdentity,
			Options: []string{tlsDisabled, tlsRequired, tlsVerifyIdentity},
			Help:    "verify-identity checks the server certificate and host name; required encrypts without verifying."},
		{Key: keyCACert, Label: "CA certificate", Type: connector.FieldText,
			Help: "PEM certificate of the CA that signed the server certificate. Leave empty to use the system roots."},
	}
}

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

	db, err := str(keyDatabase, "", true)
	if err != nil {
		return nil, err
	}
	if !validName(db, maxDatabaseLen) || strings.HasSuffix(db, " ") {
		return nil, fmt.Errorf("database must be 1 to %d characters, without a trailing space", maxDatabaseLen)
	}
	if isSystemDatabase(db) {
		return nil, fmt.Errorf("database %q is a system database", db)
	}
	out[keyDatabase] = db

	user, err := str(keyUsername, "", true)
	if err != nil {
		return nil, err
	}
	if !validName(user, maxUsernameLen) {
		return nil, fmt.Errorf("username must be 1 to %d characters", maxUsernameLen)
	}
	out[keyUsername] = user

	mode, err := str(keyTLSMode, tlsVerifyIdentity, true)
	if err != nil {
		return nil, err
	}
	switch mode {
	case tlsDisabled, tlsRequired, tlsVerifyIdentity:
	default:
		return nil, fmt.Errorf("tls_mode must be one of disabled, required, verify-identity")
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

// validName reports whether s is valid UTF-8 without NUL bytes, 1 to max
// characters long.
func validName(s string, maxChars int) bool {
	n := utf8.RuneCountInString(s)
	return utf8.ValidString(s) && n >= 1 && n <= maxChars && !strings.ContainsRune(s, 0)
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

// Validate checks one statement (PLAN.md 5.1). Every table must be in the
// connection's database; Statement.Schema is always that database.
func (*Connector) Validate(cfg connector.Config, sql string, p connector.Policy) (connector.Statement, error) {
	return validate(sql, cfg.String(keyDatabase), p)
}

// Open creates a small connection pool for one target. It does not connect
// eagerly. Dry runs and executions use their own dedicated connection.
func (c *Connector) Open(_ context.Context, cfg connector.Config, password []byte) (connector.Conn, error) {
	cfg, err := c.ValidateConfig(cfg)
	if err != nil {
		return nil, err
	}
	base, err := driverConfig(cfg, password)
	if err != nil {
		return nil, err
	}
	pc := base.Clone()
	pc.ConnectionAttributes = "program_name:" + programName
	dc, err := mysql.NewConnector(pc)
	if err != nil {
		return nil, fmt.Errorf("mysql: driver config: %w", err)
	}
	pool := sql.OpenDB(dc)
	pool.SetMaxOpenConns(poolMaxConns)
	pool.SetMaxIdleConns(poolMaxConns)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	pool.SetConnMaxLifetime(30 * time.Minute)
	return &conn{pool: pool, base: base, db: cfg.String(keyDatabase)}, nil
}

// driverConfig builds the go-sql-driver configuration. Every setting that
// matters is set explicitly rather than left to the driver's defaults.
func driverConfig(cfg connector.Config, password []byte) (*mysql.Config, error) {
	host := strings.Trim(cfg.String(keyHost), "[]")
	mc := mysql.NewConfig()
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(host, strconv.Itoa(cfg.Int(keyPort)))
	mc.DBName = cfg.String(keyDatabase)
	mc.User = cfg.String(keyUsername)
	mc.Passwd = string(password)
	mc.Loc = time.UTC
	mc.ParseTime = true
	mc.ClientFoundRows = true // rows affected means rows matched
	mc.MultiStatements = false
	mc.InterpolateParams = false
	mc.AllowAllFiles = false
	mc.AllowCleartextPasswords = false
	mc.AllowOldPasswords = false
	mc.AllowFallbackToPlaintext = false
	mc.CheckConnLiveness = true
	mc.Timeout = connectTimeout
	mc.Params = nil
	mc.DialFunc = connector.SafeDialContext(&net.Dialer{KeepAlive: 30 * time.Second})
	if err := mc.Apply(mysql.Charset("utf8mb4", "")); err != nil {
		return nil, fmt.Errorf("mysql: driver config: %w", err)
	}
	tc, err := tlsConfig(cfg, host)
	if err != nil {
		return nil, err
	}
	mc.TLS = tc
	return mc, nil
}

func tlsConfig(cfg connector.Config, host string) (*tls.Config, error) {
	mode := cfg.String(keyTLSMode)
	if mode == tlsDisabled {
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
	if mode == tlsRequired {
		// Encrypt without verifying, as MySQL's --ssl-mode=REQUIRED.
		tc.InsecureSkipVerify = true // #nosec G402 -- the admin chose "required" over "verify-identity"
	}
	return tc, nil
}

// isSystemDatabase reports whether name is one of MySQL's system schemas.
func isSystemDatabase(name string) bool {
	switch strings.ToLower(name) {
	case "mysql", "information_schema", "performance_schema", "sys":
		return true
	}
	return false
}
