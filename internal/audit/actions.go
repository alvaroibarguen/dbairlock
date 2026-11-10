package audit

// Action names an audited state change. Names are namespaced as
// "<subject>.<verb in past tense>". Every action is declared here, in one
// file, so the audit page filters and the docs have a single list.
type Action string

// Actions. Later phases add their own here.
const (
	ChangeSubmitted Action = "change.submitted"
	SecretSet       Action = "secret.set"
	SettingChanged  Action = "setting.changed"
)
