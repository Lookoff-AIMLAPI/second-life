package bridge

// State is what the tray shows.
type State string

const (
	StateOff          State = "off"
	StateStarting     State = "starting"
	StateOn           State = "on"
	StateNeedsLogin   State = "needs_login"
	StateCodexMissing State = "codex_missing"
	StateError        State = "error"
)

// Limit is one Codex usage window.
type Limit struct {
	UsedPercent int
	WindowMins  int64
	ResetsAt    int64 // unix seconds, 0 when unknown
}

// Status is a snapshot; safe to copy.
type Status struct {
	State              State
	Version            string
	SignedIn           bool
	AIMLOnly           bool // no Codex, but AIMLAPI models answer
	CodexMissing       bool
	Email, Plan        string
	Primary, Secondary *Limit

	Requests, Errors, InputTokens, OutputTokens int64
	LastModel, LastError                        string
	Addr                                        string
}
