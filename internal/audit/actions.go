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
)
