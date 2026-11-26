package tray

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Lookoff-AIMLAPI/second-life/internal/bridge"
)

var now = time.Date(2026, 11, 24, 15, 0, 0, 0, time.FixedZone("test", 3*3600)) // a Tuesday

func at(t time.Time) int64 { return t.Unix() }

func TestStates(t *testing.T) {
	cases := []struct {
		st      bridge.Status
		want    bool
		status  string
		toggle  string
		icon    iconState
		signIn  bool
		install bool
		claude  bool
	}{
		{bridge.Status{State: bridge.StateOn, Addr: "127.0.0.1:8787", SignedIn: true}, true, "On: 127.0.0.1:8787", "Turn off", iconOn, false, false, true},
		{bridge.Status{State: bridge.StateOn, SignedIn: true}, true, "On", "Turn off", iconOn, false, false, true},
		{bridge.Status{State: bridge.StateOn}, true, "On", "Turn off", iconOn, true, false, true},
		{bridge.Status{State: bridge.StateOff}, false, "Off", "Turn on", iconOff, false, false, false},
		{bridge.Status{}, false, "Off", "Turn on", iconOff, false, false, false},
		{bridge.Status{State: bridge.StateStarting}, true, "Starting", "Turn off", iconOff, false, false, false},
		{bridge.Status{State: bridge.StateNeedsLogin}, true, "Sign in needed", "Turn off", iconAttention, true, false, false},
		{bridge.Status{State: bridge.StateCodexMissing}, true, "Codex CLI not found", "Turn off", iconAttention, false, true, false},
		{bridge.Status{State: bridge.StateError, LastError: "port in use"}, false, "Error: port in use", "Turn on", iconAttention, false, false, false},
		{bridge.Status{State: bridge.StateError, LastError: "app-server died"}, true, "Error: app-server died", "Turn off", iconAttention, false, false, false},
		{bridge.Status{State: bridge.StateError}, false, "Error", "Turn on", iconAttention, false, false, false},
	}
	for _, c := range cases {
		v := buildView(snapshot{Status: c.st, Want: c.want}, now)
		name := c.status + "/" + c.toggle
		if got := v.Items[itemStatus]; got != (entry{Title: c.status, Visible: true}) {
			t.Errorf("%s: status %+v", name, got)
		}
		if got := v.Items[itemToggle]; got.Title != c.toggle || !got.Enabled || v.TurnsOn != (c.toggle == "Turn on") {
			t.Errorf("%s: toggle %+v TurnsOn=%v", name, got, v.TurnsOn)
		}
		if v.Icon != c.icon {
			t.Errorf("%s: icon %v", name, v.Icon)
		}
		if got := v.Items[itemSignIn].Visible; got != c.signIn {
			t.Errorf("%s: sign in visible %v", name, got)
		}
		if got := v.Items[itemInstall].Visible; got != c.install {
			t.Errorf("%s: install visible %v", name, got)
		}
		if got := v.Items[itemClaude]; !got.Visible || got.Enabled != c.claude {
			t.Errorf("%s: claude %+v", name, got)
		}
	}
}

func TestMenuLines(t *testing.T) {
	v := buildView(snapshot{Version: "1.2.3", Autostart: true, Status: bridge.Status{
		State: bridge.StateOn, SignedIn: true, Email: "me@example.com", Plan: "pro",
		Primary:     &bridge.Limit{UsedPercent: 34, WindowMins: 300, ResetsAt: at(now.Add(3*time.Hour + 40*time.Minute))},
		Secondary:   &bridge.Limit{UsedPercent: 61, WindowMins: 10080, ResetsAt: at(now.AddDate(0, 0, 3))},
		InputTokens: 1234567, OutputTokens: 89,
		LastError: "upstream 500",
	}}, now)
	want := map[itemID]string{
		itemPlan:      "Plan: Pro",
		itemPrimary:   "5 h limit: 34% used, resets 18:40",
		itemSecondary: "Weekly limit: 61% used, resets Fri 15:00",
		itemModels:    "Models",
		itemTokens:    "Tokens this session: 1,234,567 in, 89 out",
		itemError:     "Last error: upstream 500",
		itemAutostart: "Start at login",
		itemQuit:      "Quit",
	}
	for id, title := range want {
		if got := v.Items[id]; got.Title != title || !got.Visible {
			t.Errorf("item %d: %+v, want %q", id, got, title)
		}
	}
	for _, id := range []itemID{itemStatus, itemPlan, itemPrimary, itemSecondary, itemTokens, itemError} {
		if v.Items[id].Enabled {
			t.Errorf("info item %d is clickable", id)
		}
	}
	if !v.Items[itemAutostart].Checked {
		t.Error("start at login not checked")
	}
	if v.Tooltip != "second-life 1.2.3 - On" {
		t.Errorf("tooltip %q", v.Tooltip)
	}
	for _, e := range v.Items {
		if strings.Contains(e.Title, "@") {
			t.Errorf("email in the menu: %q", e.Title)
		}
	}
}

func TestOptionalLinesAreHidden(t *testing.T) {
	v := buildView(snapshot{}, now)
	for _, id := range []itemID{itemPlan, itemPrimary, itemSecondary, itemError, itemSignIn, itemInstall} {
		if v.Items[id].Visible {
			t.Errorf("item %d visible: %+v", id, v.Items[id])
		}
	}
	if v := buildView(snapshot{LoginBusy: true}, now); v.Items[itemSignIn].Enabled {
		t.Error("sign in is clickable twice")
	}
}

func TestLimitNames(t *testing.T) {
	for mins, want := range map[int64]string{
		0: "Limit", -5: "Limit", 30: "30 min limit", 60: "1 h limit", 90: "90 min limit",
		299: "5 h limit", 300: "5 h limit", 1440: "Daily limit", 10079: "Weekly limit",
		10080: "Weekly limit", 43200: "30 day limit",
	} {
		if got := limitName(mins); got != want {
			t.Errorf("limitName(%d) = %q, want %q", mins, got, want)
		}
	}
}

func TestResetText(t *testing.T) {
	day := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, now.Location()) }
	for _, c := range []struct {
		at   int64
		want string
	}{
		{0, "5 h limit: 1% used"},
		{at(now), "5 h limit: 1% used"},
		{at(now.Add(-time.Minute)), "5 h limit: 1% used"},
		{at(day(24, 23, 59)), "5 h limit: 1% used, resets 23:59"},
		{at(day(25, 0, 0)), "5 h limit: 1% used, resets Wed 00:00"},
		{at(day(30, 9, 5)), "5 h limit: 1% used, resets Mon 09:05"},
		{at(day(31, 9, 5)), "5 h limit: 1% used, resets 1 Dec 09:05"}, // next Tuesday
	} {
		if got := limitText(&bridge.Limit{UsedPercent: 1, WindowMins: 300, ResetsAt: c.at}, now); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	if limitText(nil, now) != "" {
		t.Error("nil limit has text")
	}
}

func TestErrorLine(t *testing.T) {
	line := func(s snapshot) string { return buildView(s, now).Items[itemError].Title }
	on := bridge.Status{State: bridge.StateOn}

	if got := line(snapshot{Status: on, ActionErr: "slife missing"}); got != "Last error: slife missing" {
		t.Errorf("action error: %q", got)
	}
	long := on
	long.LastError = strings.Repeat("я", 200)
	got := strings.TrimPrefix(line(snapshot{Status: long}), "Last error: ")
	if n := utf8.RuneCountInString(got); n != 80 || !strings.HasSuffix(got, "…") {
		t.Errorf("cut to %d runes: %q", n, got)
	}
	exact := on
	exact.LastError = strings.Repeat("a", 80)
	if got := line(snapshot{Status: exact}); got != "Last error: "+exact.LastError {
		t.Errorf("80 runes were cut: %q", got)
	}
	failed := bridge.Status{State: bridge.StateError, LastError: "port in use"}
	if got := line(snapshot{Status: failed, ActionErr: "port in use"}); got != "" {
		t.Errorf("same error twice: %q", got)
	}
	if got := line(snapshot{Status: failed, ActionErr: "other"}); got != "Last error: other" {
		t.Errorf("different error hidden: %q", got)
	}
}

func TestClean(t *testing.T) {
	if got := clean(" a\r\n\tb\x00c \xff "); got != "a b c ?" {
		t.Errorf("%q", got)
	}
	if got := cut("", 80); got != "" {
		t.Errorf("%q", got)
	}
}

func TestPlanAndModels(t *testing.T) {
	for _, c := range []struct {
		st   bridge.Status
		want string
	}{
		{bridge.Status{}, ""},
		{bridge.Status{SignedIn: true}, "Plan: unknown"},
		{bridge.Status{Plan: "self_serve_business"}, "Plan: Self serve business"},
	} {
		if got := planText(c.st); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
	got := modelLines([][2]string{{"opus", "gpt-5.6-terra"}, {"fable", "gpt-6-astra"}})
	if len(got) != 2 || got[0] != "opus: gpt-5.6-terra" {
		t.Errorf("%q", got)
	}
}

func TestCopyItems(t *testing.T) {
	for state, enabled := range map[bridge.State]bool{
		bridge.StateOn: true, bridge.StateNeedsLogin: true, bridge.StateStarting: true,
		bridge.StateOff: false, bridge.StateCodexMissing: false, bridge.StateError: false, "": false,
	} {
		st := bridge.Status{State: state, Addr: "127.0.0.1:8787"}
		v := buildView(snapshot{Status: st}, now)
		for id, title := range map[itemID]string{itemCopyAddr: "Copy API address", itemCopyKey: "Copy API key"} {
			if got := v.Items[id]; got != (entry{Title: title, Visible: true, Enabled: enabled}) {
				t.Errorf("%q: %+v", state, got)
			}
		}
		v = buildView(snapshot{Status: st, CopiedKey: true}, now)
		if v.Items[itemCopyKey].Title != "Copied" || v.Items[itemCopyAddr].Title != "Copy API address" {
			t.Errorf("%q: %+v", state, v.Items)
		}
		v = buildView(snapshot{Status: st, CopiedAddr: true}, now)
		if v.Items[itemCopyAddr].Title != "Copied" || v.Items[itemCopyKey].Title != "Copy API key" {
			t.Errorf("%q: %+v", state, v.Items)
		}
	}
	if itemCopyAddr != itemPick+1 || itemCopyKey != itemPick+2 {
		t.Error("the copy items do not follow Choose models")
	}
}

func TestAPIAddress(t *testing.T) {
	for in, want := range map[string]string{"": "", "127.0.0.1:8787": "http://127.0.0.1:8787", "http://127.0.0.1:1": "http://127.0.0.1:1"} {
		if got := apiAddress(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestDesktopItems(t *testing.T) {
	for _, c := range []struct {
		desk                         desktopState
		state                        bridge.State
		busy                         bool
		setup, setupOn, back, backOn bool
	}{
		{desktopState{}, bridge.StateOn, false, false, true, false, true},
		{desktopState{ThirdParty: true}, bridge.StateOn, false, false, true, false, true},
		{desktopState{Installed: true}, bridge.StateOn, false, true, true, false, true},
		{desktopState{true, true}, bridge.StateOn, false, true, true, true, true},
		{desktopState{true, true}, bridge.StateOff, false, true, false, true, true},
		{desktopState{true, true}, bridge.StateOn, true, true, false, true, false},
	} {
		v := buildView(snapshot{Status: bridge.Status{State: c.state, SignedIn: true}, Desktop: c.desk, DesktopBusy: c.busy}, now)
		if got := v.Items[itemDesktop]; got != (entry{Title: "Set up Claude Desktop", Visible: c.setup, Enabled: c.setupOn}) {
			t.Errorf("%+v: %+v", c, got)
		}
		if got := v.Items[itemDesktopBack]; got != (entry{Title: "Switch Claude back to claude.ai", Visible: c.back, Enabled: c.backOn}) {
			t.Errorf("%+v: %+v", c, got)
		}
		if got := v.Items[itemLog]; got != (entry{Title: "Open log", Visible: true, Enabled: true}) {
			t.Errorf("%+v", got)
		}
	}
}

func TestAIMLAPIOnlyView(t *testing.T) {
	st := bridge.Status{State: bridge.StateOn, Addr: "127.0.0.1:8787", AIMLOnly: true, CodexMissing: true}
	v := buildView(snapshot{Status: st, Want: true}, time.Now())
	if got := v.Items[itemStatus].Title; got != "On: 127.0.0.1:8787 (without Codex)" {
		t.Error(got)
	}
	if v.Items[itemSignIn].Visible || !v.Items[itemInstall].Visible || !v.Items[itemClaude].Enabled || !v.Items[itemCopyKey].Enabled {
		t.Errorf("%+v", v.Items)
	}
	st.CodexMissing = false // the CLI is there, nobody is signed in
	v = buildView(snapshot{Status: st, Want: true}, time.Now())
	if !v.Items[itemSignIn].Visible || v.Items[itemInstall].Visible {
		t.Errorf("%+v", v.Items)
	}
}

func TestPluginsItem(t *testing.T) {
	if itemPlugins != itemMCP+1 {
		t.Error("Plugins does not follow Import MCP servers")
	}
	for state, enabled := range map[bridge.State]bool{bridge.StateOn: true, bridge.StateNeedsLogin: true, bridge.StateOff: false, bridge.StateError: false} {
		v := buildView(snapshot{Status: bridge.Status{State: state}}, now)
		if got := v.Items[itemPlugins]; got != (entry{Title: "Plugins...", Visible: true, Enabled: enabled}) {
			t.Errorf("%q: %+v", state, got)
		}
		if v.Items[itemPlugins].Enabled != v.Items[itemPick].Enabled {
			t.Errorf("%q: not enabled like Choose models", state)
		}
	}
}
