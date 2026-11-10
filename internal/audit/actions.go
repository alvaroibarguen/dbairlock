package audit

// Action names an audited state change. Names are namespaced as
// "<subject>.<verb in past tense>". Every action is declared here, in one
// file, so the audit page filters and the docs have a single list.
type Action string

// Actions. Later phases add their own here.
const (
	// Changes (subject "change"). Payloads carry SQL, hashes and counts,
	// never row images.
	ChangeCreated          Action = "change.created"
	ChangeEdited           Action = "change.edited"
	ChangeSubmitted        Action = "change.submitted"
	ChangePreviewRefreshed Action = "change.preview_refreshed"
	ChangeReviewed         Action = "change.reviewed"
	ChangeApproved         Action = "change.approved"
	ChangeRejected         Action = "change.rejected"
	ChangeCancelled        Action = "change.cancelled"
	ChangeExpired          Action = "change.expired"
	// ChangeReturnedToReview: an approved change fell below its required
	// approvals before execution began (an approving reviewer was removed).
	ChangeReturnedToReview Action = "change.returned_to_review"
	ChangeApprovalsVoided  Action = "change.approvals_voided"
	ChangeExecuteRequested Action = "change.execute_requested"
	ChangeRetryRequested   Action = "change.retry_requested"

	// Executions (subject "change").
	ExecutionStarted     Action = "execution.started"
	ExecutionSucceeded   Action = "execution.succeeded"
	ExecutionFailed      Action = "execution.failed"
	ExecutionDeferred    Action = "execution.deferred"
	ExecutionInterrupted Action = "execution.interrupted"

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

	// Connections (target databases). Payloads never carry the password;
	// setting it records secret.set or secret.replaced alongside.
	ConnectionCreated  Action = "connection.created"
	ConnectionUpdated  Action = "connection.updated"
	ConnectionArchived Action = "connection.archived"
	ConnectionTested   Action = "connection.tested"
)
