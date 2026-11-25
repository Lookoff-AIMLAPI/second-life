package desktop

import (
	"os"
	"strings"
	"testing"
)

func TestFormerEntryIsTakenOver(t *testing.T) {
	f := newFixture(t)
	f.seed()
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	meta := f.path("configLibrary/_meta.json")
	id := f.json("configLibrary/_meta.json")["appliedId"].(string)
	raw, _ := os.ReadFile(meta)
	f.put(meta, strings.ReplaceAll(string(raw), entryName, formerName))
	f.put(f.path("configLibrary/"+id+".json"), `{"coworkEgressAllowedHosts":["*.mine"],"deploymentDisplayName":"Codex to Claude","inferenceCredentialHelper":"/old"}`)
	f.sys.running = true
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	m := f.json("configLibrary/_meta.json")
	if m["appliedId"] != id {
		t.Error("a second entry instead of the same one")
	}
	b, _ := os.ReadFile(meta)
	if strings.Contains(string(b), formerName) || strings.Count(string(b), entryName) != 1 {
		t.Error(string(b))
	}
	e := f.json("configLibrary/" + id + ".json")
	if e["inferenceCredentialHelper"] != f.Helper || e["deploymentDisplayName"] != entryName {
		t.Error(e["inferenceCredentialHelper"], e["deploymentDisplayName"])
	}
	if l := e["coworkEgressAllowedHosts"].([]any); len(l) != 1 || l[0] != "*.mine" {
		t.Error(l)
	}
}
