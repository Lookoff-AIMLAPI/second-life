package desktop

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

const (
	entryName  = "Second Life"
	formerName = "Codex to Claude" // the entry of the app under its first name is taken over
)

// model is one row of inferenceModels. There is no supports1m or prefer1m. Claude
// still takes these ids for 1M models and no setting gives a window in between, so
// the bridge itself asks for compaction before the model is full.
type model struct {
	Name            string `json:"name"`
	LabelOverride   string `json:"labelOverride"`
	FamilyTier      string `json:"anthropicFamilyTier"`
	IsFamilyDefault bool   `json:"isFamilyDefault"`
}

// config is one Claude Desktop configuration. Keys are flat. No key is stored:
// Claude runs the helper to get it.
type config struct {
	Provider       string   `json:"inferenceProvider"`
	BaseURL        string   `json:"inferenceGatewayBaseUrl"`
	CredentialKind string   `json:"inferenceCredentialKind"`
	Helper         string   `json:"inferenceCredentialHelper"`
	HelperArgs     []string `json:"inferenceCredentialHelperArgs"`
	Discovery      bool     `json:"modelDiscoveryEnabled"`
	ToolSearch     bool     `json:"toolSearchEnabled"`
	Effort         string   `json:"defaultModelEffort"`
	Models         []model  `json:"inferenceModels"`
	Import         imports  `json:"claudeAiImport"`
	IdleTimeout    int      `json:"inferenceStreamIdleTimeoutSec"`
	DisplayName    string   `json:"deploymentDisplayName"`
	Artifacts      bool     `json:"artifactsEnabled"`
	Designs        bool     `json:"artifactsDesignEnabled"`
	Slides         bool     `json:"artifactsSlidesEnabled"`
	Sites          []string `json:"coworkEgressAllowedHosts"`
}

// sitesKey lists the hosts Chat and Cowork may reach. Without it they reach none. A list other than "*" would also
// sandbox the shell and limit the browser of Code, so it is all or nothing here; the dialog says so.
const sitesKey = "coworkEgressAllowedHosts"

// ownersKeys are set once and then left as the owner has them.
var ownersKeys = []string{"deploymentDisplayName", "artifactsEnabled", "artifactsDesignEnabled", "artifactsSlidesEnabled", sitesKey, "toolSearchEnabled", "defaultModelEffort", "claudeAiImport"}

// imports turns on Settings > Import, where Claude itself copies the chats of the
// claude.ai mode on this computer. The originals stay where they are.
type imports struct {
	Enabled bool   `json:"enabled"`
	Banner  string `json:"bannerBehavior"`
}

// families is the display order; Claude preselects the first one. Full ids are
// needed: Desktop does not route a tier to a bare name such as "opus".
var families = [...]struct{ tier, id, label string }{
	{"opus", "claude-opus-5-5", "Opus"},
	{"sonnet", "claude-sonnet-5-5", "Sonnet"},
	{"fable", "claude-fable-5-2", "Fable"},
	{"haiku", "claude-haiku-5-5", "Haiku"},
}

func newConfig(baseURL, helper string, mapping [][2]string) config {
	c := config{
		Provider: "gateway", BaseURL: baseURL,
		CredentialKind: "helper-script", Helper: helper, HelperArgs: []string{"token"},
		ToolSearch: true, Effort: "medium",
		Import: imports{Enabled: true, Banner: "show"},
		Sites:  []string{"*"},
		// The bridge pings every 15 s and gives up after 15 minutes; a shorter limit here would repeat paid requests.
		IdleTimeout: 900, DisplayName: entryName, Artifacts: true, Designs: true, Slides: true,
	}
	for _, f := range families {
		label := f.label
		for _, m := range mapping {
			if m[0] == f.tier {
				label += " (" + upstreamLabel(m[1]) + ")"
				break
			}
		}
		c.Models = append(c.Models, model{f.id, label, f.tier, true})
	}
	return c
}

// upstreamLabel writes gpt-5.6-sol as GPT-5.6 Sol.
func upstreamLabel(id string) string {
	parts := strings.Split(id, "-")
	if len(parts) < 2 || parts[0] != "gpt" {
		return id
	}
	out := "GPT-" + parts[1]
	for _, p := range parts[2:] {
		if p != "" {
			out += " " + strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return out
}

// checkHelper repeats Claude's own rules for a helper path, so that a path it
// would refuse is caught before anything changes.
func checkHelper(path string) error {
	switch {
	case !filepath.IsAbs(path), strings.HasPrefix(path, `\\`), strings.HasPrefix(path, "//"):
		return errors.New("the slife program must be on this computer's own disk")
	case strings.ContainsAny(path, `"%`), strings.ContainsFunc(path, unicode.IsControl),
		strings.HasSuffix(path, "."), strings.HasSuffix(path, " "):
		return errors.New("Claude cannot use a path with these characters: " + path)
	}
	st, err := os.Lstat(path)
	switch {
	case err != nil:
		return errors.New("slife was not found next to the app")
	case !st.Mode().IsRegular():
		return errors.New("slife next to the app is not a plain file")
	case runtime.GOOS != "windows" && st.Mode().Perm()&0o111 == 0:
		return errors.New("slife next to the app is not allowed to run")
	}
	return nil
}

// CheckLocation refuses a copy that macOS may move or remove: the path of slife
// is stored in Claude's settings.
func CheckLocation(goos, exe string) error {
	if goos == "darwin" && !strings.HasPrefix(exe, "/Applications/") {
		return errors.New("Move Second Life to the Applications folder first, then open it from there.")
	}
	return nil
}

// StartWarning is the message for a copy that runs from a disk image or from
// the place where macOS puts a quarantined app, or "".
func StartWarning(goos, exe string) string {
	if goos == "darwin" && (strings.HasPrefix(exe, "/Volumes/") || strings.Contains(exe, "/AppTranslocation/")) {
		return "Second Life runs from a temporary place. Quit it, move it to the Applications folder and open it from there."
	}
	return ""
}

// isDesktop tells Claude Desktop from Claude Code on Windows by its path.
func isDesktop(path, local string) bool {
	p := strings.ToLower(path)
	return strings.HasPrefix(p, strings.ToLower(local)+`\anthropicclaude\`) || strings.Contains(p, `\windowsapps\claude_`)
}
