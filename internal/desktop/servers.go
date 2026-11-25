package desktop

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/mcpimport"
)

const (
	serversKey = "mcpServers"
	// localKey switches off every server of the file, in the entry and under features.
	localKey   = "isLocalDevMcpEnabled"
	maxServers = 30 // the dialog names each one
)

// server is an MCP server of the usual profile, in the bytes it has there.
type server struct {
	name string
	val  json.RawMessage
}

// addServers copies what the usual profile has and the gateway profile lacks. Nothing is replaced or removed, and
// anything unexpected means that nothing is copied: the rest of the setup does not depend on it.
func (d *Desktop) addServers(o object, off bool) (object, []server) {
	var features object
	if raw := o.get("features"); raw != nil {
		features, _ = parseObject(raw)
	}
	if d.Usual == "" || off || string(features.get(localKey)) == "false" {
		return o, nil
	}
	have := object{}
	if raw := o.get(serversKey); raw != nil {
		var err error
		if have, err = parseObject(raw); err != nil {
			return o, nil
		}
	}
	data, existed, _, err := readFile(d.Usual)
	if err != nil || !existed {
		return o, nil
	}
	usual, err := parseObject(data)
	if err != nil {
		return o, nil
	}
	from, err := parseObject(usual.get(serversKey))
	if err != nil {
		return o, nil
	}
	m, err := mcpimport.Here()
	if err != nil {
		return o, nil
	}
	var add []server
	for _, e := range from {
		if len(add) == maxServers {
			break
		}
		if has(have, e.key) {
			continue
		}
		raw := from.get(e.key) // the last one, when the name is there twice
		in, err := parseObject(raw)
		if err != nil || in.get("extensionId") != nil {
			continue
		}
		out := object{}
		for _, key := range []string{"command", "args", "env"} { // all that Claude reads
			if v := in.get(key); v != nil {
				out = append(out, member{key, v})
			}
		}
		val, err := out.encode()
		if err != nil {
			continue
		}
		// What Claude does not accept would give a warning at every start.
		if s, ok := mcpimport.Local(e.key, val); !ok || m.Unusable(s) != "" {
			continue
		}
		have = append(have, member{e.key, val})
		add = append(add, server{e.key, val})
	}
	if len(add) == 0 {
		return o, nil
	}
	val, err := have.encode()
	if err != nil {
		return o, nil
	}
	out, err := o.with(serversKey, json.RawMessage(val))
	if err != nil {
		return o, nil
	}
	return out, add
}

// has ignores case, as Claude does when it looks for a taken name.
func has(o object, name string) bool {
	for _, m := range o {
		if strings.EqualFold(m.key, name) {
			return true
		}
	}
	return false
}

// switchedOff reads the flag the way Claude does: what it does not understand counts as off.
func switchedOff(raw json.RawMessage) bool {
	var v any
	if raw == nil || json.Unmarshal(raw, &v) != nil || v == nil {
		return false
	}
	switch v := v.(type) {
	case bool:
		return !v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s != "" && s != "true" && s != "1"
	case float64:
		return v != 1
	}
	return true
}

// announced: each of now was in the dialog and is still the same.
func announced(now, dialog []server) bool {
	for _, s := range now {
		found := false
		for _, a := range dialog {
			found = found || s.name == a.name && sameJSON(s.val, a.val)
		}
		if !found {
			return false
		}
	}
	return true
}

func sameJSON(a, b json.RawMessage) bool {
	if bytes.Equal(a, b) {
		return true
	}
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

func serversText(list []server) string {
	var names []string
	for _, s := range list { // the names are checked
		names = append(names, s.name)
	}
	n := strconv.Itoa(len(list)) + " MCP servers"
	if len(list) == 1 {
		n = "one MCP server"
	}
	return "It also copies " + n + " from your usual Claude to this mode: " + strings.Join(names, ", ") +
		". They are programs that Claude starts on this computer, and they keep the keys you gave them. \"slife mcp list\" in a terminal shows their commands."
}
