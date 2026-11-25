package tray

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
)

const maxErrorRunes = 80

type iconState int

const (
	iconOff iconState = iota
	iconOn
	iconAttention
)

type itemID int

// Menu items in the order they appear. Separators are added in menu.go.
const (
	itemStatus itemID = iota
	itemToggle
	itemPlan
	itemPrimary
	itemSecondary
	itemModels
	itemPick
	itemCopyAddr
	itemCopyKey
	itemTokens
	itemError
	itemSignIn
	itemInstall
	itemClaude
	itemMCP
	itemDesktop
	itemDesktopBack
	itemLog
	itemAutostart
	itemQuit
	itemCount
)

type entry struct {
	Title                     string
	Enabled, Visible, Checked bool
}

// snapshot is everything the menu depends on.
type snapshot struct {
	Status                bridge.Status
	Autostart             bool
	ActionErr             string // last failed menu action, "" when the last one worked
	LoginBusy             bool
	Want                  bool // the owner wants it on and Start has not failed
	CopiedAddr, CopiedKey bool // show "Copied" for a moment
	Desktop               desktopState
	DesktopBusy           bool
	Version               string
}

// desktopState is what the menu shows of Claude Desktop.
type desktopState struct{ Installed, ThirdParty bool }

type view struct {
	Icon    iconState
	Tooltip string
	TurnsOn bool // what a click on the toggle does
	Items   [itemCount]entry
}

func buildView(s snapshot, now time.Time) view {
	st := s.Status
	v := view{Icon: iconFor(st.State), TurnsOn: !s.Want}
	status := statusText(st)
	v.Tooltip = strings.TrimSpace("second-life "+clean(s.Version)) + " - " + stateWord(st.State)

	info := func(id itemID, title string) {
		v.Items[id] = entry{Title: title, Visible: title != ""}
	}
	action := func(id itemID, title string, visible, enabled bool) {
		v.Items[id] = entry{Title: title, Visible: visible, Enabled: enabled}
	}

	toggle := "Turn off"
	if v.TurnsOn {
		toggle = "Turn on"
	}

	info(itemStatus, status)
	action(itemToggle, toggle, true, true)
	info(itemPlan, planText(st))
	info(itemPrimary, limitText(st.Primary, now))
	info(itemSecondary, limitText(st.Secondary, now))
	action(itemModels, "Models", true, true)
	action(itemPick, "Choose models...", true, st.State != bridge.StateOff && st.State != bridge.StateError)
	api := st.State == bridge.StateOn || st.State == bridge.StateNeedsLogin || st.State == bridge.StateStarting
	action(itemCopyAddr, copyTitle("Copy API address", s.CopiedAddr), true, api)
	action(itemCopyKey, copyTitle("Copy API key", s.CopiedKey), true, api)
	info(itemTokens, fmt.Sprintf("Tokens this session: %s in, %s out", group(st.InputTokens), group(st.OutputTokens)))
	info(itemError, lastErrorText(s, status))
	action(itemSignIn, "Sign in with ChatGPT", signedOut(st), !s.LoginBusy)
	action(itemInstall, "Install Codex CLI", st.State == bridge.StateCodexMissing || st.CodexMissing, true)
	action(itemClaude, "Open Claude", true, st.State == bridge.StateOn)
	action(itemMCP, "Import MCP servers...", true, true)
	action(itemDesktop, "Set up Claude Desktop", s.Desktop.Installed, st.State == bridge.StateOn && !s.DesktopBusy)
	action(itemDesktopBack, "Switch Claude back to claude.ai", s.Desktop.Installed && s.Desktop.ThirdParty, !s.DesktopBusy)
	action(itemLog, "Open log", true, true)
	action(itemAutostart, "Start at login", true, true)
	v.Items[itemAutostart].Checked = s.Autostart
	action(itemQuit, "Quit", true, true)
	return v
}

func iconFor(s bridge.State) iconState {
	switch s {
	case bridge.StateOn:
		return iconOn
	case bridge.StateNeedsLogin, bridge.StateCodexMissing, bridge.StateError:
		return iconAttention
	}
	return iconOff
}

func copyTitle(title string, copied bool) string {
	if copied {
		return "Copied"
	}
	return title
}

func stateWord(s bridge.State) string {
	switch s {
	case bridge.StateOn:
		return "On"
	case bridge.StateStarting:
		return "Starting"
	case bridge.StateNeedsLogin:
		return "Sign in needed"
	case bridge.StateCodexMissing:
		return "Codex CLI not found"
	case bridge.StateError:
		return "Error"
	}
	return "Off"
}

func statusText(st bridge.Status) string {
	word := stateWord(st.State)
	switch st.State {
	case bridge.StateOn:
		if addr := clean(strings.TrimPrefix(st.Addr, "http://")); addr != "" {
			if st.AIMLOnly {
				addr += " (without Codex)"
			}
			return word + ": " + addr
		}
	case bridge.StateError:
		if msg := errorText(st.LastError); msg != "" {
			return word + ": " + msg
		}
	}
	return word
}

func lastErrorText(s snapshot, status string) string {
	msg := errorText(s.ActionErr)
	if msg == "" {
		msg = errorText(s.Status.LastError)
	}
	// The status line already shows it.
	if msg == "" || status == "Error: "+msg {
		return ""
	}
	return "Last error: " + msg
}

// signedOut says whether the bridge knows nobody is signed in. While it is off,
// starting or failed, SignedIn is false only because nothing asked Codex yet.
func signedOut(st bridge.Status) bool {
	return st.State == bridge.StateNeedsLogin || (st.State == bridge.StateOn && !st.SignedIn && !st.CodexMissing)
}

// planText never includes the email: menus end up in screenshots.
func planText(st bridge.Status) string {
	plan := clean(strings.ReplaceAll(st.Plan, "_", " "))
	if plan == "" {
		if st.SignedIn {
			return "Plan: unknown"
		}
		return ""
	}
	r := []rune(plan)
	r[0] = unicode.ToUpper(r[0])
	return "Plan: " + string(r)
}

func limitText(l *bridge.Limit, now time.Time) string {
	if l == nil {
		return ""
	}
	text := fmt.Sprintf("%s: %d%% used", limitName(l.WindowMins), l.UsedPercent)
	if at := time.Unix(l.ResetsAt, 0).In(now.Location()); l.ResetsAt > 0 && at.After(now) {
		text += ", resets " + resetText(at, now)
	}
	return text
}

func limitName(mins int64) string {
	const day = 24 * 60
	// Codex rounds down at times: 299 for 5 hours, 10079 for a week.
	if mins >= 59 && (mins+1)%60 == 0 {
		mins++
	}
	switch {
	case mins <= 0:
		return "Limit"
	case mins == 7*day:
		return "Weekly limit"
	case mins == day:
		return "Daily limit"
	case mins%day == 0:
		return fmt.Sprintf("%d day limit", mins/day)
	case mins%60 == 0:
		return fmt.Sprintf("%d h limit", mins/60)
	}
	return fmt.Sprintf("%d min limit", mins)
}

func resetText(at, now time.Time) string {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case at.Before(today.AddDate(0, 0, 1)):
		return at.Format("15:04")
	case at.Before(today.AddDate(0, 0, 7)):
		return at.Format("Mon 15:04")
	}
	return at.Format("2 Jan 15:04")
}

// modelLines turns the bridge's mapping into submenu titles.
func modelLines(mapping [][2]string) []string {
	lines := make([]string, 0, len(mapping))
	for _, m := range mapping {
		lines = append(lines, clean(m[0])+": "+clean(m[1]))
	}
	return lines
}

func errorText(s string) string {
	return cut(clean(s), maxErrorRunes)
}

// clean makes a string safe for a menu: valid UTF-8 (AppKit throws on anything
// else), one line, no control characters (Windows refuses NUL).
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, "?"))
	return strings.Join(strings.Fields(s), " ")
}

func cut(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

// group writes 1234567 as 1,234,567.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, s = "-", s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}
