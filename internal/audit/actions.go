package audit

// Action names an audited state change. Names are namespaced as
// "<subject>.<verb in past tense>". Every action is declared here, in one
// file, so the audit page filters and the docs have a single list.
type Action string

// Actions. Later phases add their own here.
const (
	ChangeSubmitted Action = "change.submitted"

	// Secrets. Payloads carry the secret's reference, never its value.
	SecretSet      Action = "secret.set"
	SecretReplaced Action = "secret.replaced"
	SecretCleared  Action = "secret.cleared"

	// Master key lifecycle.
	MasterKeyInitialized Action = "master_key.initialized"
	MasterKeyRotated     Action = "master_key.rotated"

	SettingChanged Action = "setting.changed"

	// First-run setup and recovery.
	SetupCodeIssued      Action = "setup.code_issued"
	SetupCodeAccepted    Action = "setup.code_accepted"
	SetupCodeRejected    Action = "setup.code_rejected"
	SetupCompleted       Action = "setup.completed"
	RecoveryIssued       Action = "setup.recovery_issued"
	RecoveryCodeAccepted Action = "setup.recovery_code_accepted"
	RecoveryCodeRejected Action = "setup.recovery_code_rejected"
	RecoveryCompleted    Action = "setup.recovery_completed"

	// Sign-in, sessions, users and API tokens.
	LoginSucceeded      Action = "login.succeeded"
	LoginDenied         Action = "login.denied"
	DevLogin            Action = "login.dev"
	SignedOut           Action = "session.signed_out"
	SignedOutEverywhere Action = "session.signed_out_everywhere"
	UserCreated         Action = "user.created"
	UserRoleChanged     Action = "user.role_changed"
	UserRemoved         Action = "user.removed"
	UserRestored        Action = "user.restored"
	APITokenCreated     Action = "api_token.created"
	APITokenRevoked     Action = "api_token.revoked"
)
