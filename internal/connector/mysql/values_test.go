package mysql

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

func TestDisplayValue(t *testing.T) {
	ts := time.Date(2024, 3, 4, 3, 6, 7, 123456000, time.UTC)
	cases := []struct {
		v      any
		typ    string
		max    int
		want   string // JSON
		cutoff bool
	}{
		{nil, "VARCHAR", 0, `null`, false},
		{int64(42), "INT", 0, `42`, false},
		{int64(1 << 60), "BIGINT", 0, `"1152921504606846976"`, false},
		{uint64(1 << 63), "UNSIGNED BIGINT", 0, `"9223372036854775808"`, false},
		{uint64(7), "UNSIGNED INT", 0, `7`, false},
		{float64(1.5), "DOUBLE", 0, `"1.5"`, false},
		{float32(0.1), "FLOAT", 0, `"0.1"`, false},
		{ts, "TIMESTAMP", 0, `"2024-03-04T03:06:07.123456Z"`, false},
		{ts, "DATETIME", 0, `"2024-03-04T03:06:07.123456"`, false},
		{time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), "DATE", 0, `"2024-01-02"`, false},
		{[]byte("12.50"), "DECIMAL", 0, `"12.50"`, false},
		{[]byte(`{"a": 1}`), "JSON", 0, `{"a":1}`, false},
		{[]byte(`{"a": 12345678901234567890}`), "JSON", 0, `{"a":12345678901234567890}`, false},
		{[]byte(`[1,2,3,4,5,6,7,8]`), "JSON", 4, `{"type":"text","value":"[1,2","size":17,"truncated":true}`, true},
		{[]byte{0, 0xff}, "BLOB", 0, `{"type":"bytes","value":"AP8=","size":2}`, false},
		{[]byte{0, 0xff, 1}, "VARBINARY", 2, `{"type":"bytes","value":"AP8=","size":3,"truncated":true}`, true},
		{[]byte("héllo"), "VARCHAR", 2, `{"type":"text","value":"h","size":6,"truncated":true}`, true},
		{[]byte{0xff, 0xfe}, "TEXT", 0, `{"type":"bytes","value":"//4=","size":2}`, false},
		{[]byte("12:00:00"), "TIME", 0, `"12:00:00"`, false},
	}
	for _, c := range cases {
		got, cut := displayValue(c.v, c.typ, c.max)
		raw, err := json.Marshal(got)
		if err != nil {
			t.Errorf("%v (%s): %v", c.v, c.typ, err)
			continue
		}
		if string(raw) != c.want || cut != c.cutoff {
			t.Errorf("displayValue(%v, %s, %d) = %s, %v; want %s, %v", c.v, c.typ, c.max, raw, cut, c.want, c.cutoff)
		}
	}
}

func TestCanonicalCell(t *testing.T) {
	berlin := time.FixedZone("CET", 3600)
	cases := []struct {
		v    any
		want connector.Cell
	}{
		{nil, connector.NullCell()},
		{[]byte(""), connector.BytesCell([]byte(""))},
		{[]byte{0, 1}, connector.BytesCell([]byte{0, 1})},
		{int64(-3), connector.BytesCell([]byte("-3"))},
		{uint64(18446744073709551615), connector.BytesCell([]byte("18446744073709551615"))},
		{float64(0.1), connector.BytesCell([]byte("0.1"))},
		{time.Date(2024, 1, 2, 4, 0, 0, 0, berlin), connector.BytesCell([]byte("2024-01-02 03:00:00"))},
		{time.Date(2024, 1, 2, 3, 0, 0, 500, time.UTC), connector.BytesCell([]byte("2024-01-02 03:00:00.0000005"))},
	}
	for _, c := range cases {
		got := canonicalCell(c.v)
		if got.Null != c.want.Null || string(got.Data) != string(c.want.Data) {
			t.Errorf("canonicalCell(%v) = %+v, want %+v", c.v, got, c.want)
		}
	}
	// NULL and the empty string differ; so do the same instant in two zones
	// only if they are different instants.
	if connector.Fingerprint([]connector.FingerprintRow{{Values: []connector.Cell{canonicalCell(nil)}}}) ==
		connector.Fingerprint([]connector.FingerprintRow{{Values: []connector.Cell{canonicalCell([]byte(""))}}}) {
		t.Error("NULL and empty string have the same fingerprint")
	}
}

func TestLabel(t *testing.T) {
	for in, want := range map[string]string{
		"dbairlock change #123":  "dbairlock change #123",
		"":                       "dbairlock",
		"a,b:c":                  "a?b:c",
		"tab\there":              "tab?here",
		"ünï":                    "?n?",
		strings.Repeat("x", 100): strings.Repeat("x", 64),
	} {
		if got := label(in); got != want {
			t.Errorf("label(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPinSQLMode(t *testing.T) {
	for in, want := range map[string]string{
		"ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION": "ONLY_FULL_GROUP_BY,STRICT_TRANS_TABLES,NO_ZERO_IN_DATE,NO_ZERO_DATE,ERROR_FOR_DIVISION_BY_ZERO,NO_ENGINE_SUBSTITUTION",
		"REAL_AS_FLOAT,PIPES_AS_CONCAT,ANSI_QUOTES,IGNORE_SPACE,ONLY_FULL_GROUP_BY,ANSI":                                        "REAL_AS_FLOAT,ONLY_FULL_GROUP_BY",
		"NO_BACKSLASH_ESCAPES,HIGH_NOT_PRECEDENCE":                                                                              "",
		"": "",
	} {
		got, err := pinSQLMode(in)
		if err != nil || got != want {
			t.Errorf("pinSQLMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := pinSQLMode("STRICT_TRANS_TABLES'; DROP TABLE x; --"); err == nil {
		t.Error("an unexpected sql_mode was accepted")
	}
}

func TestParseServerInfo(t *testing.T) {
	cases := []struct {
		version, comment string
		want             connector.ServerInfo
	}{
		{"8.4.13", "MySQL Community Server - GPL", connector.ServerInfo{Product: "MySQL", Version: "8.4.13", Major: 8, Supported: true}},
		{"9.7.4", "MySQL Community Server - GPL", connector.ServerInfo{Product: "MySQL", Version: "9.7.4", Major: 9, Supported: true}},
		{"8.0.40", "MySQL Community Server - GPL", connector.ServerInfo{Product: "MySQL", Version: "8.0.40", Major: 8}},
		{"26.10.0", "MySQL Community Server - GPL", connector.ServerInfo{Product: "MySQL", Version: "26.10.0", Major: 26}},
		{"9.6.0", "", connector.ServerInfo{Product: "MySQL", Version: "9.6.0", Major: 9}},
		{"11.4.2-MariaDB-ubu2404", "mariadb.org binary distribution", connector.ServerInfo{Product: "MariaDB", Version: "11.4.2-MariaDB-ubu2404", Major: 11}},
		{"8.4.0-mariadb", "", connector.ServerInfo{Product: "MariaDB", Version: "8.4.0-mariadb", Major: 8}},
		{"garbage", "", connector.ServerInfo{Product: "MySQL", Version: "garbage"}},
	}
	for _, c := range cases {
		if got := parseServerInfo(c.version, c.comment); got != c.want {
			t.Errorf("parseServerInfo(%q) = %+v, want %+v", c.version, got, c.want)
		}
	}
}

func TestAnalyzeGrants(t *testing.T) {
	least := []string{
		"GRANT USAGE ON *.* TO `dbairlock_app`@`%`",
		"GRANT SELECT, INSERT, UPDATE, DELETE ON `app`.* TO `dbairlock_app`@`%`",
	}
	var rep connector.PrivilegeReport
	analyzeGrants(&rep, least, "app")
	if len(rep.Excess) != 0 || !rep.CanSelect || !rep.CanInsert || !rep.CanUpdate || !rep.CanDelete {
		t.Errorf("least privilege report = %+v", rep)
	}

	rep = connector.PrivilegeReport{}
	analyzeGrants(&rep, []string{"GRANT SELECT ON `other`.* TO `u`@`%`", "GRANT UPDATE (`name`) ON `app`.`users` TO `u`@`%`"}, "app")
	if rep.CanSelect || !rep.CanUpdate || rep.CanInsert || len(rep.Excess) != 0 {
		t.Errorf("column grant report = %+v", rep)
	}

	rep = connector.PrivilegeReport{}
	analyzeGrants(&rep, []string{"GRANT SELECT ON `app\\_%`.* TO `u`@`%`"}, "app_x")
	if !rep.CanSelect {
		t.Errorf("wildcard database grant not matched: %+v", rep)
	}

	rep = connector.PrivilegeReport{}
	analyzeGrants(&rep, []string{
		"GRANT ALL PRIVILEGES ON *.* TO `root`@`%` WITH GRANT OPTION",
		"GRANT SUPER, CREATE, ALTER, DROP, INDEX, FILE, PROCESS ON *.* TO `ops`@`%`",
		"GRANT INSERT, UPDATE, DELETE ON *.* TO `w`@`%`",
		"GRANT BACKUP_ADMIN,SYSTEM_VARIABLES_ADMIN ON *.* TO `root`@`%`",
		"GRANT PROXY ON ``@`` TO `root`@`%` WITH GRANT OPTION",
		"GRANT `admin_role`@`%` TO `u`@`%`",
		"GRANT EXECUTE ON PROCEDURE `app`.`p` TO `u`@`%`",
	}, "app")
	want := []string{
		"ALL PRIVILEGES ON *.*", "GRANT OPTION ON *.*",
		"SUPER ON *.*", "CREATE ON *.*", "ALTER ON *.*", "DROP ON *.*", "INDEX ON *.*", "FILE ON *.*", "PROCESS ON *.*",
		"INSERT ON *.*", "UPDATE ON *.*", "DELETE ON *.*",
		"BACKUP_ADMIN, SYSTEM_VARIABLES_ADMIN ON *.*",
		"PROXY ON ``@``", "GRANT OPTION ON ``@``",
		"ROLE `admin_role`@`%`",
		"EXECUTE ON PROCEDURE `app`.`p`",
	}
	got := map[string]connector.ExcessPrivilege{}
	for _, x := range rep.Excess {
		got[x.Privilege] = x
		if x.Detail == "" {
			t.Errorf("%s has no detail", x.Privilege)
		}
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("excess %q not reported; got %v", w, keys(got))
		}
	}
	if fix := got["SUPER ON *.*"].Fix; fix != "REVOKE SUPER ON *.* FROM `ops`@`%`;" {
		t.Errorf("SUPER fix = %q", fix)
	}
	if !rep.CanSelect || !rep.CanDelete {
		t.Errorf("ALL PRIVILEGES on *.* does not count as select/delete: %+v", rep)
	}

	rep = connector.PrivilegeReport{}
	analyzeGrants(&rep, []string{"something odd"}, "app")
	if len(rep.Excess) != 1 || rep.Excess[0].Privilege != "UNPARSED GRANT" {
		t.Errorf("unparsed grant report = %+v", rep)
	}
}

func keys(m map[string]connector.ExcessPrivilege) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestRoleList(t *testing.T) {
	for in, want := range map[string]bool{
		"`r1`@`%`":               true,
		"`r1`@`%`,`r``2`@`host`": true,
		"`r1`@`%`; DROP TABLE x": false,
		"r1@%":                   false,
		"`r1`@`%`,":              false,
		"`a`@`%` UNION SELECT 1": false,
	} {
		if got := roleListRe.MatchString(in); got != want {
			t.Errorf("roleListRe(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestValidateConfig(t *testing.T) {
	c := New()
	good, err := c.ValidateConfig(connector.Config{keyHost: "db.internal", keyDatabase: "app", keyUsername: "u"})
	if err != nil {
		t.Fatal(err)
	}
	if good.Int(keyPort) != 3306 || good.String(keyTLSMode) != tlsVerifyIdentity {
		t.Errorf("defaults = %+v", good)
	}
	for name, cfg := range map[string]connector.Config{
		"password in config": {keyHost: "h", keyDatabase: "app", keyUsername: "u", keyPassword: "x"},
		"unknown field":      {keyHost: "h", keyDatabase: "app", keyUsername: "u", "schema": "x"},
		"no host":            {keyDatabase: "app", keyUsername: "u"},
		"metadata address":   {keyHost: "169.254.169.254", keyDatabase: "app", keyUsername: "u"},
		"bad port":           {keyHost: "h", keyPort: 70000, keyDatabase: "app", keyUsername: "u"},
		"system database":    {keyHost: "h", keyDatabase: "mysql", keyUsername: "u"},
		"long database":      {keyHost: "h", keyDatabase: strings.Repeat("d", 65), keyUsername: "u"},
		"long user":          {keyHost: "h", keyDatabase: "app", keyUsername: strings.Repeat("u", 33)},
		"bad tls":            {keyHost: "h", keyDatabase: "app", keyUsername: "u", keyTLSMode: "verify-full"},
		"bad ca":             {keyHost: "h", keyDatabase: "app", keyUsername: "u", keyCACert: "not pem"},
		"host with slash":    {keyHost: "h/x", keyDatabase: "app", keyUsername: "u"},
	} {
		if _, err := c.ValidateConfig(cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestDriverConfig(t *testing.T) {
	cfg, err := New().ValidateConfig(connector.Config{keyHost: "db.internal", keyDatabase: "app", keyUsername: "u", keyTLSMode: tlsRequired})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := driverConfig(cfg, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	if !mc.ClientFoundRows || mc.MultiStatements || !mc.ParseTime || mc.InterpolateParams || mc.Loc != time.UTC ||
		mc.AllowAllFiles || mc.AllowCleartextPasswords || mc.AllowFallbackToPlaintext || mc.DialFunc == nil {
		t.Errorf("driver config = %+v", mc)
	}
	if mc.TLS == nil || !mc.TLS.InsecureSkipVerify || mc.Addr != "db.internal:3306" || mc.DBName != "app" {
		t.Errorf("driver config = %+v", mc)
	}
}

func TestQuoteIdent(t *testing.T) {
	if got := quoteQualified("a`b", "c"); got != "`a``b`.`c`" {
		t.Errorf("quoteQualified = %s", got)
	}
}
