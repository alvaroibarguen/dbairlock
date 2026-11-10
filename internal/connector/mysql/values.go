package mysql

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alvaroibarguen/dbairlock/internal/connector"
)

// Every query the connector runs uses the text protocol, under session
// settings pinned per transaction (time_zone '+00:00', character set
// utf8mb4). The driver returns integers as int64 or uint64, floating point
// numbers as float32 or float64, DATE, DATETIME and TIMESTAMP values as
// time.Time in UTC (parseTime with loc=UTC), and everything else as the
// server's own text or raw bytes: DECIMAL in MySQL's canonical text form,
// JSON in MySQL's normalized form, binary strings as raw bytes.
//
// canonicalCell turns each of these into fingerprint bytes that do not
// depend on session settings; displayValue turns them into JSON-safe values
// for the preview.

// canonicalTime is the fingerprint form of a temporal value, in UTC.
const canonicalTime = "2006-01-02 15:04:05.999999999"

// canonicalCell converts a driver value to its fingerprint form.
func canonicalCell(v any) connector.Cell {
	switch x := v.(type) {
	case nil:
		return connector.NullCell()
	case []byte:
		return connector.BytesCell(x)
	case string:
		return connector.BytesCell([]byte(x))
	case int64:
		return connector.BytesCell(strconv.AppendInt(nil, x, 10))
	case uint64:
		return connector.BytesCell(strconv.AppendUint(nil, x, 10))
	case float32:
		return connector.BytesCell(strconv.AppendFloat(nil, float64(x), 'g', -1, 32))
	case float64:
		return connector.BytesCell(strconv.AppendFloat(nil, x, 'g', -1, 64))
	case time.Time:
		return connector.BytesCell([]byte(x.UTC().Format(canonicalTime)))
	case bool:
		return connector.BytesCell([]byte(strconv.FormatBool(x)))
	default:
		return connector.BytesCell(fmt.Appendf(nil, "%T:%v", v, v))
	}
}

// maxSafeInt is the largest integer a JSON number holds exactly in a
// browser.
const maxSafeInt = 1 << 53

// displayValue converts a driver value of a column whose database type name
// (as the driver reports it, e.g. "VARCHAR", "BLOB", "TIMESTAMP") is
// dbType to a JSON-safe display value, truncating to maxBytes. The bool
// reports truncation.
func displayValue(v any, dbType string, maxBytes int) (any, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case int64:
		if x <= maxSafeInt && x >= -maxSafeInt {
			return x, false
		}
		return strconv.FormatInt(x, 10), false
	case uint64:
		if x <= maxSafeInt {
			return int64(x), false
		}
		return strconv.FormatUint(x, 10), false
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), false
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), false
	case time.Time:
		return timeValue(x, dbType), false
	case bool:
		return x, false
	case string:
		return connector.TextValue(x, maxBytes)
	case []byte:
		return bytesValue(x, dbType, maxBytes)
	default:
		return connector.TextValue(fmt.Sprint(v), maxBytes)
	}
}

func timeValue(t time.Time, dbType string) string {
	t = t.UTC()
	switch strings.ToUpper(dbType) {
	case "DATE":
		return t.Format("2006-01-02")
	case "TIMESTAMP":
		return t.Format(time.RFC3339Nano)
	default: // DATETIME has no time zone
		return t.Format("2006-01-02T15:04:05.999999999")
	}
}

func bytesValue(b []byte, dbType string, maxBytes int) (any, bool) {
	switch strings.ToUpper(dbType) {
	case "BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "GEOMETRY", "BIT", "VECTOR":
		v, truncated := connector.BytesValue(b, maxBytes)
		return v, truncated
	case "JSON":
		if maxBytes > 0 && len(b) > maxBytes {
			return connector.TextValue(string(b), maxBytes)
		}
		dec := json.NewDecoder(strings.NewReader(string(b)))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil && !dec.More() {
			return v, false
		}
	}
	if !utf8.Valid(b) {
		v, truncated := connector.BytesValue(b, maxBytes)
		return v, truncated
	}
	// DECIMAL, character strings, ENUM, SET, TIME, YEAR and the like: the
	// server's text form.
	return connector.TextValue(string(b), maxBytes)
}
