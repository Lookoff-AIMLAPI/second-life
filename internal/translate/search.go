package translate

import (
	"encoding/json"
	"strings"

	"github.com/Lookoff-AIMLAPI/second-life/internal/dto"
)

const (
	reqSearchPrefix = "web_search_"
	reqNoResult     = "[tool call interrupted: no result]"
	reqServerResult = "_tool_result" // suffix of the results of tools that run on Anthropic's side
	reqServerLimit  = 4000           // characters kept of one such result
)

// reqServerBlocks are the other blocks of a server-tool turn.
var reqServerBlocks = []string{"server_tool_use", "mcp_tool_use", "mcp_tool_listing", "container_upload", "fallback"}

const reqEncrypted = "reasoning.encrypted_content"

func reqInclude(search bool) []string {
	out := []string{reqEncrypted}
	if search {
		out = append(out, "web_search_call.action.sources")
	}
	return out
}

func reqWebSearch(t dto.Tool) dto.WebSearchTool {
	w := dto.WebSearchTool{Type: "web_search"}
	f := map[string]any{}
	if len(t.AllowedDomains) > 0 {
		f["allowed_domains"] = t.AllowedDomains
	}
	if len(t.BlockedDomains) > 0 {
		f["blocked_domains"] = t.BlockedDomains
	}
	if len(f) > 0 {
		w.Filters = f
	}
	if !reqNull(t.UserLocation) {
		w.UserLocation = t.UserLocation
	}
	return w
}

// serverText keeps a server tool result readable for the model; empty when there is nothing to read.
func (b *reqBuilder) serverText(block dto.Block) string {
	var content any
	if json.Unmarshal(block.Content, &content) != nil {
		content = nil
	}
	label := strings.ReplaceAll(strings.TrimSuffix(block.Type, reqServerResult), "_", " ")
	fields, _ := content.(map[string]any)
	if code, ok := fields["error_code"].(string); ok {
		if len(code) > reqNameMax || reqClean(code) != code {
			code = "unknown"
		}
		return "[" + label + " failed: " + code + "]"
	}
	if block.Type == "web_search_tool_result" {
		return reqSearchText(content)
	}
	if block.Type == "tool_search_tool_result" {
		var lines []string
		for _, name := range reqToolRefs(block) {
			lines = append(lines, b.available(name))
		}
		if lines == nil {
			return "[tool search found no tools]"
		}
		return strings.Join(lines, "\n")
	}
	var lines []string
	take := func(m map[string]any, keys ...string) {
		for _, k := range keys {
			if v, _ := m[k].(string); v != "" {
				lines = append(lines, v)
			}
		}
	}
	switch c := content.(type) {
	case string:
		lines = append(lines, c)
	case []any: // mcp: a list of text blocks
		for _, v := range c {
			m, _ := v.(map[string]any)
			take(m, "text")
		}
	case map[string]any: // advisor, code execution, web fetch
		take(c, "text", "title", "url", "stdout", "stderr")
		doc, _ := c["content"].(map[string]any)
		take(doc, "title")
		if src, _ := doc["source"].(map[string]any); src["type"] == "text" {
			take(src, "data")
		}
	}
	text := []rune(strings.TrimSpace(strings.Join(lines, "\n")))
	if len(text) == 0 {
		return ""
	}
	if len(text) > reqServerLimit {
		text = append(text[:reqServerLimit], []rune(" [cut]")...)
	}
	return "[" + label + " result]\n" + string(text)
}

// reqSearchText lists the pages of an earlier search.
func reqSearchText(content any) string {
	list, _ := content.([]any)
	var b strings.Builder
	for _, v := range list {
		m, _ := v.(map[string]any)
		title, _ := m["title"].(string)
		if url, _ := m["url"].(string); url != "" {
			b.WriteString("\n" + strings.TrimSpace(title+" "+url))
		}
	}
	if b.Len() == 0 {
		return "[web search found nothing]"
	}
	return "[web search results]" + b.String()
}

// reqToolRefs names the tools a server-side tool search found.
func reqToolRefs(block dto.Block) []string {
	if block.Type != "tool_search_tool_result" {
		return nil
	}
	var c struct {
		Refs []struct {
			Name string `json:"tool_name"`
		} `json:"tool_references"`
	}
	if json.Unmarshal(block.Content, &c) != nil {
		return nil
	}
	var names []string
	for _, r := range c.Refs {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	return names
}

// reqSearchResult turns a search_result block into text.
func reqSearchResult(raw json.RawMessage, at string) (string, *dto.APIError) {
	var r struct {
		Source  string          `json:"source"`
		Title   string          `json:"title"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return "", reqInvalid("%s: malformed content block", at)
	}
	lines := []string{"[search result]"}
	for _, s := range []string{r.Title, r.Source} {
		if s != "" {
			lines = append(lines, s)
		}
	}
	if !reqNull(r.Content) {
		blocks, err := reqBlocks(r.Content, at+".content", "text")
		if err != nil {
			return "", err
		}
		for _, b := range blocks {
			lines = append(lines, b.Text)
		}
	}
	return strings.Join(lines, "\n"), nil
}

// reqLooseResult keeps the content of a tool result that has no call. Files stay with the user role.
func reqLooseResult(output any) []map[string]any {
	const head = "[result of an earlier tool call]"
	parts, ok := output.([]map[string]any)
	if !ok {
		text, _ := output.(string)
		return []map[string]any{reqMessage("user", reqText("input_text", head+"\n"+text))}
	}
	texts := []map[string]any{reqText("input_text", head)}
	var files []map[string]any
	for _, p := range parts {
		if p["type"] == "input_text" {
			texts = append(texts, p)
		} else {
			files = append(files, p)
		}
	}
	return []map[string]any{reqMessage("user", texts...), reqMessage("user", files...)}
}

// stSources reads the query and the pages of a finished web_search_call.
func stSources(item map[string]any) (query string, urls []string) {
	a := stMap(item["action"])
	query = stStr(a["query"])
	if query == "" {
		if qs, _ := a["queries"].([]any); len(qs) > 0 {
			query = stStr(qs[0])
		}
	}
	seen := map[string]bool{}
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	add(stStr(a["url"]))
	list, _ := a["sources"].([]any)
	for _, v := range list {
		add(stStr(stMap(v)["url"]))
	}
	if query == "" && len(urls) > 0 {
		query = urls[0]
	}
	return query, urls
}
