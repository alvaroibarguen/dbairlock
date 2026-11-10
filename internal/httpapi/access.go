package httpapi

import (
	"github.com/alvaroibarguen/dbairlock/internal/identity"
)

// Level is who may reach a route at all.
type Level string

// Access levels.
const (
	// LevelPublic routes answer anyone. Each limits itself: health probes and
	// instance metadata reveal nothing private, the sign-in routes only start
	// or finish an OpenID flow, and sign-out only clears the caller's own
	// cookie.
	LevelPublic Level = "public"
	// LevelSetup routes work only in setup mode (or after `dbairlock
	// recover`), and only with the setup code, a recovery code or the
	// short-lived setup session those codes grant. The setup module enforces
	// this; the codes are rate limited.
	LevelSetup Level = "setup"
	// LevelDev routes exist only with --dev and answer 404 otherwise.
	LevelDev Level = "dev"
	// LevelUser routes need a signed-in dashboard session. If Access.Action
	// is set, the user must also be allowed that action (identity.Can).
	LevelUser Level = "user"
	// LevelToken routes need an API token (MCP). They are not part of the
	// REST API and never accept a session cookie.
	LevelToken Level = "token"
)

// Access declares who may call one route.
type Access struct {
	Level  Level
	Action identity.Action // with LevelUser: the action required, "" for any signed-in user
}

var (
	public    = Access{Level: LevelPublic}
	setupOnly = Access{Level: LevelSetup}
	devOnly   = Access{Level: LevelDev}
	anyUser   = Access{Level: LevelUser}
)

func allowed(a identity.Action) Access { return Access{Level: LevelUser, Action: a} }

// operationAccess declares the access level of every operation in
// api/openapi.yaml, by operationId. The authenticate middleware enforces it
// before any handler runs (handlers check again, with the resource), and
// refuses an operation that is missing here. The route access test in
// internal/app walks every registered route and fails on one without a
// declaration.
var operationAccess = map[string]Access{
	// System
	"GetHealthz": public,
	"GetReadyz":  public,
	"GetMeta":    public,

	// Setup and recovery
	"VerifySetupCode":    setupOnly,
	"VerifyRecoveryCode": setupOnly,
	"GetSetupSession":    setupOnly,
	"GetSetupSignIn":     setupOnly,
	"PutSetupSignIn":     setupOnly,
	"StartSetupGoogle":   setupOnly,

	// Sign-in and sessions
	"StartGoogleSignIn": public,
	"GoogleCallback":    public,
	"SignOut":           public,
	"SignOutEverywhere": anyUser,
	"DevLogin":          devOnly,
	"GetMe":             anyUser,

	// Users and tokens
	"ListUsers":   allowed(identity.ActionUserManage),
	"UpdateUser":  allowed(identity.ActionUserManage),
	"RemoveUser":  allowed(identity.ActionUserManage),
	"RestoreUser": allowed(identity.ActionUserManage),
	"ListTokens":  anyUser, // own tokens; ?all=true needs user.manage (checked by the module)
	"CreateToken": anyUser,
	"RevokeToken": anyUser, // own tokens; others' need user.manage (checked by the module)

	// OAuth consent page and connected apps. The protocol endpoints
	// (/oauth/*, /.well-known/*) are outside the contract (OAuthRoutes).
	"GetOAuthRequest":    anyUser,
	"DecideOAuthRequest": anyUser, // the decision acts as the signed-in person
	"ListOAuthGrants":    anyUser, // own apps; ?all=true needs user.manage (checked by the module)
	"RevokeOAuthGrant":   anyUser, // own apps; others' need user.manage (checked by the module)

	// Settings
	"GetSettings":    allowed(identity.ActionSettingsManage),
	"UpdateSettings": allowed(identity.ActionSettingsManage),

	// Connections
	"ListConnectors":         allowed(identity.ActionChangeSubmit),
	"ListConnections":        allowed(identity.ActionChangeSubmit),
	"GetConnection":          allowed(identity.ActionChangeSubmit),
	"GetConnectionSchema":    allowed(identity.ActionChangeSubmit),
	"CreateConnection":       allowed(identity.ActionDatabaseManage),
	"UpdateConnection":       allowed(identity.ActionDatabaseManage),
	"ArchiveConnection":      allowed(identity.ActionDatabaseManage),
	"TestConnection":         allowed(identity.ActionDatabaseManage),
	"TestConnectionSettings": allowed(identity.ActionDatabaseManage),

	// Changes: the changes module checks authorship and review rules.
	"ListChanges":          anyUser,
	"CreateChange":         anyUser,
	"PreviewChange":        anyUser,
	"GetChange":            anyUser,
	"UpdateChange":         anyUser,
	"SubmitChange":         anyUser,
	"CancelChange":         anyUser,
	"RefreshChangePreview": anyUser,
	"ReviewChange":         anyUser,
	"ExecuteChange":        anyUser,
	"RetryChange":          anyUser,
	"ListChangeEvents":     anyUser,

	// Sequences: the changes module checks authorship, and every step is a
	// change with the rules above.
	"CreateSequence":     anyUser,
	"GetSequence":        anyUser,
	"CancelSequence":     anyUser,
	"ListSequenceEvents": anyUser,

	// Audit, system and notifications
	"ListAuditEvents":      allowed(identity.ActionAuditRead),
	"GetAuditVerification": allowed(identity.ActionSettingsManage),
	"VerifyAudit":          allowed(identity.ActionSettingsManage),
	"ExportAudit":          allowed(identity.ActionSettingsManage),
	"GetSystem":            allowed(identity.ActionSettingsManage),
	"ListNotifications":    allowed(identity.ActionSettingsManage),
	"SendTestNotification": allowed(identity.ActionSettingsManage),
}

// OperationAccess returns the declared access of an operation (by
// operationId, as the generated code names it).
func OperationAccess(operationID string) (Access, bool) {
	a, ok := operationAccess[operationID]
	return a, ok
}
