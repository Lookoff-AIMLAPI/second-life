// Package desktop switches Claude Desktop between claude.ai and this bridge.
//
// Claude Desktop keeps third-party settings in its own folder, Claude-3p:
// configLibrary/<id>.json and configLibrary/_meta.json hold the configurations,
// claude_desktop_config.json holds the mode, developer_settings.json holds the
// developer mode flag.
package desktop

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// System is everything outside the settings folder.
type System interface {
	Installed() bool
	// Managed says whether an administrator's policy is in force.
	Managed() (bool, error)
	Running() (bool, error)
	// Quit asks Claude to quit; it may refuse.
	Quit() error
	Open() error
	// Confirm shows a question; false means the owner declined.
	Confirm(title, text, button string) (bool, error)
	// Alert shows a message.
	Alert(title, text string)
}

// ErrCancelled means the owner declined.
var ErrCancelled = errors.New("cancelled")

// Desktop is one installation of Claude Desktop.
type Desktop struct {
	Sys       System
	Dir       string // the Claude-3p folder
	Usual     string // the settings file of the usual profile, read only; "" copies no servers
	BackupDir string
	BaseURL   string
	GOOS, Exe string
	Helper    string // the slife program
	Blocked   func() error

	quitWait, poll time.Duration
	now            func() time.Time
	write          writeFunc
}

const (
	title        = "Second Life"
	modeFile     = "claude_desktop_config.json"
	developer    = "developer_settings.json"
	developerKey = "allowDevTools"
	modeKey      = "deploymentMode"
	thirdParty   = "3p"
	firstParty   = "1p"
)

var idPattern = regexp.MustCompile(`^[a-f0-9-]{36}$`) // Claude's own

// ThirdParty says whether Claude is set to gateway mode. It is for display only.
func (d *Desktop) ThirdParty() bool {
	data, existed, _, err := readFile(filepath.Join(d.Dir, modeFile))
	if err != nil || !existed {
		return false
	}
	o, err := parseObject(data)
	if err != nil {
		return false
	}
	mode, _ := o.str(modeKey)
	return mode == thirdParty
}

// plan is what a switch will write.
type plan struct {
	changes   []change
	developer bool // turns developer mode on
	sites     bool // lets Claude's tools reach every website
	servers   []server
}

// Setup points Claude Desktop at the bridge and opens it.
func (d *Desktop) Setup(ctx context.Context, mapping [][2]string) error {
	if err := CheckLocation(d.GOOS, d.Exe); err != nil {
		return err
	}
	if err := checkHelper(d.Helper); err != nil {
		return err
	}
	newID, err := newUUID()
	if err != nil {
		return err
	}
	cfg := newConfig(d.BaseURL, d.Helper, mapping)
	text := func(p plan) string {
		s := "Claude will quit and reopen in gateway mode, where the models you chose answer through this app. Open Claude sessions stop.\n\n" +
			"Gateway mode keeps its own chats and settings. \"Switch Claude back to claude.ai\" in this menu returns to the usual ones.\n\n" +
			"To see your usual chats there, use Settings > Import & export > Import in Claude. It copies the list. Code chats keep one shared transcript: deleting an imported one can delete the original too."
		if p.sites {
			s += "\n\nIt also lets Claude's tools open every website in this mode. Without that, Chat and Cowork open none. The risk: text on a page can talk the model into sending data anywhere. You can narrow it in Claude under Setup > Allowed egress hosts; your choice is kept."
		}
		if p.developer {
			s += "\n\nThis also turns on Claude's developer mode, which shows its developer tools."
		}
		if len(p.servers) > 0 {
			s += "\n\n" + serversText(p.servers)
		}
		return s
	}
	return d.run(ctx, "Set Up Claude", text, func() (plan, error) { return d.planSetup(cfg, newID) })
}

// Back returns Claude Desktop to claude.ai and opens it.
func (d *Desktop) Back(ctx context.Context) error {
	text := func(plan) string {
		return "Claude will quit and reopen signed in to claude.ai. Open Claude sessions stop."
	}
	return d.run(ctx, "Switch Back", text, func() (plan, error) {
		c, err := d.planMode(firstParty)
		return plan{changes: []change{c}}, err
	})
}

// run is the common flow. Everything is planned twice: before the question, so
// that nothing is asked in vain, and again once Claude is gone, since it may
// save its settings while it quits.
func (d *Desktop) run(ctx context.Context, button string, text func(plan) string, build func() (plan, error)) (err error) {
	managed, err := d.Sys.Managed()
	if err != nil {
		return fmt.Errorf("could not check for a company policy: %w", err)
	}
	if managed {
		return errors.New("Claude is managed by a company policy on this computer, so its settings were not changed")
	}
	if d.Blocked != nil {
		if err := d.Blocked(); err != nil {
			return err
		}
	}
	first, err := d.plan(build)
	if err != nil {
		return err
	}
	if ok, err := d.Sys.Confirm(title, text(first), button); err != nil {
		return err
	} else if !ok {
		return ErrCancelled
	}
	running, err := d.Sys.Running()
	if err != nil {
		return err
	}
	if running {
		if err := d.quit(ctx); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				d.Sys.Open() // it was open before, and its settings are as they were
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := d.plan(build)
	if err != nil {
		return err
	}
	if p.developer && !first.developer || p.sites && !first.sites || !announced(p.servers, first.servers) {
		return errors.New("Claude's settings changed meanwhile, try again")
	}
	if err := backup(d.BackupDir, d.clock().Format("20060102-150405.000"), d.Dir, p.changes); err != nil {
		return fmt.Errorf("could not back up Claude's settings: %w", err)
	}
	write := d.write
	if write == nil {
		write = writeAtomic
	}
	undo, err := apply(p.changes, write)
	if err != nil {
		return err
	}
	prune(d.BackupDir)
	if err := d.Sys.Open(); err != nil {
		return errors.Join(fmt.Errorf("Claude did not open, its old settings are back: %w", err), undo())
	}
	return nil
}

func (d *Desktop) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// plan drops the changes that change nothing.
func (d *Desktop) plan(build func() (plan, error)) (plan, error) {
	for _, dir := range []string{d.Dir, filepath.Join(d.Dir, "configLibrary")} {
		if err := checkDir(dir); err != nil {
			return plan{}, unchanged(err)
		}
	}
	p, err := build()
	if err != nil {
		return plan{}, unchanged(err)
	}
	p.changes = slices.DeleteFunc(p.changes, func(c change) bool { return c.existed && string(c.old) == string(c.data) })
	return p, nil
}

func unchanged(err error) error {
	return fmt.Errorf("Claude's settings were left alone: %w", err)
}

func (d *Desktop) quit(ctx context.Context) error {
	wait, poll := d.quitWait, d.poll
	if wait == 0 {
		wait, poll = 30*time.Second, 500*time.Millisecond
	}
	if err := d.Sys.Quit(); err != nil {
		return fmt.Errorf("could not ask Claude to quit: %w", err)
	}
	deadline := time.After(wait)
	for {
		running, err := d.Sys.Running()
		if err != nil {
			return err
		}
		if !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return errors.New("Claude did not quit, so nothing was changed. Quit it yourself (on Windows from its icon near the clock) and try again.")
		case <-time.After(poll):
		}
	}
}

func (d *Desktop) planMode(mode string) (change, error) {
	return edit(filepath.Join(d.Dir, modeFile), func(o object) (object, error) { return o.with(modeKey, mode) })
}

// planSetup orders the writes so that the mode comes last: until then Claude
// ignores the rest.
func (d *Desktop) planSetup(cfg config, newID string) (plan, error) {
	lib := filepath.Join(d.Dir, "configLibrary")
	id := ""
	meta, err := edit(filepath.Join(lib, "_meta.json"), func(o object) (object, error) {
		var out object
		var err error
		out, id, err = editMeta(o, newID)
		return out, err
	})
	if err != nil {
		return plan{}, err
	}
	// The connection is ours and is replaced; what the owner changed or added in
	// Claude stays. A broken entry means something unexpected happened to it.
	p, localOff := plan{}, false
	entry, err := edit(filepath.Join(lib, id+".json"), func(old object) (object, error) {
		raw, err := marshal(cfg)
		if err != nil {
			return nil, err
		}
		ours, err := parseObject(raw)
		if err != nil {
			return nil, err
		}
		p.sites = old.get(sitesKey) == nil
		localOff = switchedOff(old.get(localKey))
		out := object{}
		for _, m := range ours {
			if v := old.get(m.key); v != nil && slices.Contains(ownersKeys, m.key) && string(v) != `"`+formerName+`"` {
				m.val = v
			}
			out = append(out, m)
		}
		for _, m := range old {
			if out.get(m.key) == nil && !strings.HasPrefix(m.key, "inference") {
				out = append(out, m)
			}
		}
		return out, nil
	})
	if err != nil {
		return plan{}, err
	}
	p.changes = []change{entry, meta}
	dev, err := edit(filepath.Join(d.Dir, developer), func(o object) (object, error) {
		p.developer = string(o.get(developerKey)) != "true"
		if !p.developer {
			return o, nil
		}
		return o.with(developerKey, true)
	})
	if err != nil {
		return plan{}, err
	}
	if p.developer {
		p.changes = append(p.changes, dev)
	}
	mode, err := edit(filepath.Join(d.Dir, modeFile), func(o object) (object, error) {
		o, p.servers = d.addServers(o, localOff)
		return o.with(modeKey, thirdParty)
	})
	if err != nil {
		return plan{}, err
	}
	p.changes = append(p.changes, mode)
	return p, nil
}

// editMeta makes our entry the applied one, adding it if it is not there.
func editMeta(o object, newID string) (out object, id string, err error) {
	if raw := o.get("appliedId"); raw != nil {
		if _, ok := o.str("appliedId"); !ok {
			return nil, "", errors.New("appliedId is not text")
		}
	}
	var entries []json.RawMessage
	if raw := o.get("entries"); raw != nil {
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			return nil, "", errors.New("entries is not a list")
		}
	} else if len(o) > 0 {
		return nil, "", errors.New("entries is missing")
	}
	old, oldID := -1, ""
	for n, raw := range entries {
		e, err := parseObject(raw)
		if err != nil {
			return nil, "", errors.New("an entry is not an object")
		}
		eid, ok := e.str("id")
		if !ok {
			return nil, "", errors.New("an entry has no id")
		}
		name, _ := e.str("name")
		if name == entryName && id == "" {
			if !idPattern.MatchString(eid) {
				return nil, "", errors.New("the id of our entry is not valid")
			}
			id = eid
		}
		if name == formerName && old < 0 && idPattern.MatchString(eid) {
			old, oldID = n, eid
		}
	}
	if id == "" && old >= 0 {
		id = oldID
		e, err := parseObject(entries[old])
		if err == nil {
			e, err = e.with("name", entryName)
		}
		if err == nil {
			entries[old], err = e.encode()
		}
		if err != nil {
			return nil, "", err
		}
	}
	if id == "" {
		id = newID
		raw, err := marshal(struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}{id, entryName})
		if err != nil {
			return nil, "", err
		}
		entries = append(entries, raw)
	}
	if out, err = o.with("appliedId", id); err == nil {
		out, err = out.with("entries", entries)
	}
	// Claude drops it too when another entry is applied; while it is there, it
	// wins over the entry.
	return out.without("hybridPointer"), id, err
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
