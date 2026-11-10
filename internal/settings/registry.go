package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/alvaroibarguen/dbairlock/internal/secrets"
)

// Type is the type of a setting's value.
type Type string

// Types. Go values are string, string, int, bool and []string respectively.
const (
	TypeString     Type = "string"
	TypeURL        Type = "url"
	TypeInt        Type = "int"
	TypeBool       Type = "bool"
	TypeStringList Type = "string_list"
)

// Key is a typed handle on a registered setting, used with Get.
type Key[T any] struct{ name string }

// Name returns the setting's key, for example "auth.session_hours".
func (k Key[T]) Name() string { return k.name }

// Registered settings. Keep this list small: every entry is a support burden.
var (
	InstanceName      = Key[string]{"instance.name"}
	InstancePublicURL = Key[string]{"instance.public_url"}
	// TrustedProxies lists the CIDR ranges of reverse proxies whose
	// X-Forwarded-For header is believed.
	TrustedProxies = Key[[]string]{"instance.trusted_proxies"}

	GoogleClientID = Key[string]{"auth.google.client_id"}
	AllowedDomains = Key[[]string]{"auth.allowed_domains"}
	SessionHours   = Key[int]{"auth.session_hours"}
	// OIDCIssuer is Google's issuer. It can point elsewhere only on a loopback
	// address, which needs access to the host itself: that is how tests and
	// the end-to-end suite use a fake provider. It is not shown in Settings.
	OIDCIssuer = Key[string]{"auth.oidc_issuer"}

	DefaultRequiredApprovals = Key[int]{"policy.default_required_approvals"}
	DefaultMaxRows           = Key[int]{"policy.default_max_rows"}
	DefaultApprovalTTLHours  = Key[int]{"policy.default_approval_ttl_hours"}
	DefaultStoreRowImages    = Key[bool]{"policy.default_store_row_images"}
	DefaultAutoExecute       = Key[bool]{"policy.default_auto_execute"}
	PreviewMaxAgeMinutes     = Key[int]{"policy.preview_max_age_minutes"}

	StatementTimeoutMS = Key[int]{"limits.statement_timeout_ms"}
	LockTimeoutMS      = Key[int]{"limits.lock_timeout_ms"}
	// Per API token limits of the MCP server.
	MCPCallsPerMinute     = Key[int]{"limits.mcp_calls_per_minute"}
	MCPPreviewsPerHour    = Key[int]{"limits.mcp_previews_per_hour"}
	MCPSubmissionsPerHour = Key[int]{"limits.mcp_submissions_per_hour"}

	NotifyEnabled = Key[bool]{"notify.enabled"}
	// NotifyFormat is the shape of the webhook body: NotifyFormats.
	NotifyFormat = Key[string]{"notify.format"}
	// NotifyEvents lists the events that are sent: NotifyEventNames.
	NotifyEvents = Key[[]string]{"notify.events"}
)

// NotifyFormats are the accepted values of notify.format.
var NotifyFormats = []string{"generic", "slack"}

// NotifyEventNames are the accepted entries of notify.events.
var NotifyEventNames = []string{
	"change.submitted", "change.approved", "change.rejected", "change.executed", "change.failed",
}

// Definition declares one setting. Min and Max bound an int (Min 1 on a
// string means it is required); MaxLen bounds a string and each list item;
// MaxItems bounds a list.
type Definition struct {
	Key      string
	Type     Type
	Default  any
	Min, Max int
	MaxLen   int
	MaxItems int
	// Public settings may be shown before sign-in (for example on the
	// sign-in page).
	Public bool
	// check validates and normalizes a decoded value.
	check func(any) (any, error)
}

const (
	maxURLLen    = 2048
	maxDomainLen = 253
	maxCIDRLen   = 49 // the longest IPv6 prefix in text form
)

// GoogleIssuer is the OpenID Connect issuer of Google accounts.
const GoogleIssuer = "https://accounts.google.com"

var definitions = []Definition{
	{Key: InstanceName.name, Type: TypeString, Default: "dbairlock", Min: 1, MaxLen: 64, Public: true, check: checkName},
	{Key: InstancePublicURL.name, Type: TypeURL, Default: "", MaxLen: maxURLLen, check: checkPublicURL},
	{Key: TrustedProxies.name, Type: TypeStringList, Default: []string{}, MaxLen: maxCIDRLen, MaxItems: 50, check: checkCIDRs},

	{Key: GoogleClientID.name, Type: TypeString, Default: "", MaxLen: 255, check: checkNoSpaces},
	{Key: AllowedDomains.name, Type: TypeStringList, Default: []string{}, MaxLen: maxDomainLen, MaxItems: 50, check: checkDomains},
	{Key: SessionHours.name, Type: TypeInt, Default: 12, Min: 1, Max: 720},
	{Key: OIDCIssuer.name, Type: TypeURL, Default: GoogleIssuer, Min: 1, MaxLen: maxURLLen, check: checkIssuer},

	{Key: DefaultRequiredApprovals.name, Type: TypeInt, Default: 1, Min: 1, Max: 5},
	{Key: DefaultMaxRows.name, Type: TypeInt, Default: 100, Min: 1, Max: 10000},
	// Up to 30 days: an approval older than that should be resubmitted.
	{Key: DefaultApprovalTTLHours.name, Type: TypeInt, Default: 72, Min: 1, Max: 720},
	{Key: DefaultStoreRowImages.name, Type: TypeBool, Default: true},
	{Key: DefaultAutoExecute.name, Type: TypeBool, Default: true},
	{Key: PreviewMaxAgeMinutes.name, Type: TypeInt, Default: 5, Min: 1, Max: 60},

	// 100 ms to 5 minutes: a row-level change that needs longer is not one.
	{Key: StatementTimeoutMS.name, Type: TypeInt, Default: 10000, Min: 100, Max: 300000},
	// 100 ms to 1 minute: waiting longer for a lock blocks production traffic.
	{Key: LockTimeoutMS.name, Type: TypeInt, Default: 3000, Min: 100, Max: 60000},
	// Every MCP tool call, then the dry runs (each a real transaction on the
	// target) and the submissions among them.
	{Key: MCPCallsPerMinute.name, Type: TypeInt, Default: 60, Min: 1, Max: 1000},
	{Key: MCPPreviewsPerHour.name, Type: TypeInt, Default: 30, Min: 1, Max: 1000},
	{Key: MCPSubmissionsPerHour.name, Type: TypeInt, Default: 20, Min: 1, Max: 1000},

	{Key: NotifyEnabled.name, Type: TypeBool, Default: false},
	{Key: NotifyFormat.name, Type: TypeString, Default: "generic", Min: 1, MaxLen: 16, check: oneOf(NotifyFormats)},
	{Key: NotifyEvents.name, Type: TypeStringList, Default: slices.Clone(NotifyEventNames), MaxLen: 32, MaxItems: 5, check: someOf(NotifyEventNames)},
}

// oneOf accepts exactly one of allowed.
func oneOf(allowed []string) func(any) (any, error) {
	return func(v any) (any, error) {
		s := strings.TrimSpace(v.(string))
		if !slices.Contains(allowed, s) {
			return nil, invalidf("Must be one of: %s.", strings.Join(allowed, ", "))
		}
		return s, nil
	}
}

// someOf accepts a list whose entries are all in allowed, de-duplicated and
// in the order of allowed.
func someOf(allowed []string) func(any) (any, error) {
	return func(v any) (any, error) {
		in := v.([]string)
		for _, s := range in {
			if !slices.Contains(allowed, s) {
				return nil, invalidf("%q is not one of: %s.", s, strings.Join(allowed, ", "))
			}
		}
		out := []string{}
		for _, s := range allowed {
			if slices.Contains(in, s) {
				out = append(out, s)
			}
		}
		return out, nil
	}
}

// Definitions returns every registered setting in registry order.
func Definitions() []Definition { return slices.Clone(definitions) }

func definition(key string) (Definition, bool) {
	i := slices.IndexFunc(definitions, func(d Definition) bool { return d.Key == key })
	if i < 0 {
		return Definition{}, false
	}
	return definitions[i], true
}

// Secrets that belong with settings. They live in the secret store, never in
// the settings table; settings only describe, set and clear them. identity
// and notify open them by these references.
var (
	GoogleClientSecret = secrets.Ref{Kind: secrets.KindOAuthClientSecret, OwnerType: "instance", OwnerID: "google_oauth"}
	NotifyWebhookURL   = secrets.Ref{Kind: secrets.KindWebhookURL, OwnerType: "instance", OwnerID: "notify_webhook"}
)

// SecretSlot names a settings-related secret as the API sees it.
type SecretSlot struct {
	Name string
	Ref  secrets.Ref
}

var secretSlots = []SecretSlot{
	{Name: "auth.google.client_secret", Ref: GoogleClientSecret},
	{Name: "notify.webhook_url", Ref: NotifyWebhookURL},
}

// SecretSlots returns the settings-related secrets.
func SecretSlots() []SecretSlot { return slices.Clone(secretSlots) }

func secretSlot(name string) (SecretSlot, bool) {
	i := slices.IndexFunc(secretSlots, func(s SecretSlot) bool { return s.Name == name })
	if i < 0 {
		return SecretSlot{}, false
	}
	return secretSlots[i], true
}

// invalid is a validation failure whose text is shown to the user as is.
type invalid string

func (m invalid) Error() string { return string(m) }

func invalidf(format string, args ...any) error { return invalid(fmt.Sprintf(format, args...)) }

const (
	msgRequired  = invalid("A value is required.")
	msgWholeNum  = invalid("Must be a whole number.")
	msgText      = invalid("Must be text.")
	msgBool      = invalid("Must be true or false.")
	msgTextList  = invalid("Must be a list of text values.")
	msgBadJSON   = invalid("Value is not valid JSON.")
	msgNoSpaces  = invalid("Must not contain spaces.")
	msgNoControl = invalid("Must not contain control characters.")
)

// parse decodes raw (a json.RawMessage or any Go value that marshals to the
// right JSON type), checks the type and bounds, and normalizes it.
func (d Definition) parse(raw any) (any, error) {
	data, ok := raw.(json.RawMessage)
	if !ok {
		var err error
		if data, err = json.Marshal(raw); err != nil {
			return nil, msgBadJSON
		}
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, msgRequired
	}
	v, err := d.decode(data)
	if err != nil {
		return nil, err
	}
	if d.check != nil {
		if v, err = d.check(v); err != nil {
			return nil, err
		}
	}
	return v, d.bounds(v)
}

func (d Definition) decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, msgBadJSON
	}
	switch d.Type {
	case TypeString, TypeURL:
		s, ok := v.(string)
		if !ok {
			return nil, msgText
		}
		return s, nil
	case TypeInt:
		n, ok := v.(json.Number)
		if !ok {
			return nil, msgWholeNum
		}
		i, err := strconv.Atoi(n.String())
		if err != nil {
			return nil, msgWholeNum
		}
		return i, nil
	case TypeBool:
		b, ok := v.(bool)
		if !ok {
			return nil, msgBool
		}
		return b, nil
	case TypeStringList:
		items, ok := v.([]any)
		if !ok {
			return nil, msgTextList
		}
		out := make([]string, 0, len(items))
		for _, it := range items {
			s, ok := it.(string)
			if !ok {
				return nil, msgTextList
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("settings: unknown type %q", d.Type)
}

func (d Definition) bounds(v any) error {
	switch x := v.(type) {
	case int:
		if x < d.Min || x > d.Max {
			return invalidf("Must be between %d and %d.", d.Min, d.Max)
		}
	case string:
		n := len([]rune(x))
		if d.MaxLen > 0 && n > d.MaxLen {
			return invalidf("Must be at most %d characters.", d.MaxLen)
		}
		if n < d.Min {
			return msgRequired
		}
	case []string:
		if d.MaxItems > 0 && len(x) > d.MaxItems {
			return invalidf("At most %d entries.", d.MaxItems)
		}
		for _, s := range x {
			if d.MaxLen > 0 && len([]rune(s)) > d.MaxLen {
				return invalidf("Each entry must be at most %d characters.", d.MaxLen)
			}
		}
	}
	return nil
}

func checkName(v any) (any, error) {
	s := strings.TrimSpace(v.(string))
	if strings.ContainsFunc(s, unicode.IsControl) {
		return nil, msgNoControl
	}
	return s, nil
}

func checkNoSpaces(v any) (any, error) {
	s := strings.TrimSpace(v.(string))
	if strings.ContainsFunc(s, unicode.IsSpace) || strings.ContainsFunc(s, unicode.IsControl) {
		return nil, msgNoSpaces
	}
	return s, nil
}

// checkPublicURL accepts an empty value (not configured yet) or an absolute
// http(s) URL with a host and no credentials, query or fragment. A trailing
// slash is removed.
func checkPublicURL(v any) (any, error) {
	s := strings.TrimSpace(v.(string))
	if s == "" {
		return "", nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, invalid("Must be an absolute http:// or https:// URL, for example https://dbairlock.example.com.")
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, invalid("Must not contain credentials, a query or a fragment.")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// checkIssuer accepts Google's issuer, or an http(s) URL on a loopback
// address. A remote issuer other than Google would let an admin mint
// identities for any account (and so, for example, approve their own
// change); a loopback one needs host access, which already grants
// everything.
func checkIssuer(v any) (any, error) {
	s := strings.TrimRight(strings.TrimSpace(v.(string)), "/")
	if s == GoogleIssuer {
		return s, nil
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, invalid("Must be " + GoogleIssuer + ".")
	}
	if !IsLoopbackHost(u.Hostname()) {
		return nil, invalid("Must be " + GoogleIssuer + ". Another issuer is accepted only on a loopback address, for tests.")
	}
	return s, nil
}

// IsLoopbackHost reports whether host is "localhost" or a loopback IP.
func IsLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	return err == nil && a.IsLoopback()
}

// checkCIDRs normalizes a list of CIDR ranges; a bare address means just
// that address.
func checkCIDRs(v any) (any, error) {
	in := v.([]string)
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		p, err := netip.ParsePrefix(s)
		if err != nil {
			a, aerr := netip.ParseAddr(s)
			if aerr != nil {
				return nil, invalidf("%q is not an IP range, for example 10.0.0.0/8.", s)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		p = p.Masked()
		if !slices.Contains(out, p.String()) {
			out = append(out, p.String())
		}
	}
	return out, nil
}

// ParsePrefixes parses a validated instance.trusted_proxies value. Entries
// that do not parse (which validation rules out) are skipped.
func ParsePrefixes(list []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, s := range list {
		if p, err := netip.ParsePrefix(s); err == nil {
			out = append(out, p)
		}
	}
	return out
}

var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// checkDomains lower-cases, trims and de-duplicates domain names and rejects
// anything that is not a DNS name with at least two labels.
func checkDomains(v any) (any, error) {
	in := v.([]string)
	out := make([]string, 0, len(in))
	for _, d := range in {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		if len(d) > maxDomainLen || !domainPattern.MatchString(d) {
			return nil, invalidf("%q is not a domain name, for example example.com.", d)
		}
		if !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	return out, nil
}
