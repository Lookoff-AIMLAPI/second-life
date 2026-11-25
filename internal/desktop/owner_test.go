package desktop

import (
	"reflect"
	"strings"
	"testing"
)

func TestOwnerChoicesSurviveAnUpdate(t *testing.T) {
	for name, hosts := range map[string]string{"own list": `["*.corp.example"]`, "none on purpose": `[]`, "all": `["*"]`} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.seed()
			if err := f.setup(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(f.sys.text, "every website") {
				t.Error("the first setup must announce the open access")
			}
			id := f.json("configLibrary/_meta.json")["appliedId"].(string)
			entry := f.path("configLibrary/" + id + ".json")
			f.put(entry, `{"coworkEgressAllowedHosts":`+hosts+`,"defaultModelEffort":"high","toolSearchEnabled":false,"autoModeClassifierModel":"x","inferenceGatewayApiKey":"old","inferenceGatewayBaseUrl":"http://old"}`)
			f.sys.running = true
			if err := f.setup(); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(f.sys.text, "every website") {
				t.Error("announced a change that is not made")
			}
			got := f.json("configLibrary/" + id + ".json")
			want := map[string]any{"defaultModelEffort": "high", "toolSearchEnabled": false, "autoModeClassifierModel": "x", "inferenceGatewayBaseUrl": "http://127.0.0.1:8787"}
			for k, v := range want {
				if !reflect.DeepEqual(got[k], v) {
					t.Errorf("%s: %v", k, got[k])
				}
			}
			if _, kept := got["inferenceGatewayApiKey"]; kept {
				t.Error("an old key stayed")
			}
			list, _ := got["coworkEgressAllowedHosts"].([]any)
			if (name == "none on purpose") != (len(list) == 0) || name == "own list" && list[0] != "*.corp.example" {
				t.Error(list)
			}
		})
	}
}

// An entry of an older version has no list yet: the dialog must say what is added.
func TestOlderEntryGetsSitesOnlyWithAWarning(t *testing.T) {
	f := newFixture(t)
	f.seed()
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	id := f.json("configLibrary/_meta.json")["appliedId"].(string)
	f.put(f.path("configLibrary/"+id+".json"), `{"inferenceProvider":"gateway"}`)
	f.sys.running = true
	if err := f.setup(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.sys.text, "every website") {
		t.Error("no warning")
	}
	// The list disappears between the question and the write: nothing may be opened unannounced.
	f.put(f.path("configLibrary/"+id+".json"), `{"coworkEgressAllowedHosts":[]}`)
	f.sys.running = true
	f.sys.onQuit = func() { f.put(f.path("configLibrary/"+id+".json"), `{"inferenceProvider":"gateway"}`) }
	if err := f.setup(); err == nil {
		t.Error("opened every site without saying so")
	}
	if _, set := f.json("configLibrary/" + id + ".json")["coworkEgressAllowedHosts"]; set {
		t.Error("written anyway")
	}
}
