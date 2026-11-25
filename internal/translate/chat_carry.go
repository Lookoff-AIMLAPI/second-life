package translate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
)

// What a Chat server needs back on the next turn travels through Claude in the two fields a client returns untouched:
// the signature of a thinking block and the id of a tool call. It is bound to the model that produced it.

const (
	chatSignature = "c2r1."
	chatCall      = "c2x1_"
	chatMaxID     = 40 // OpenAI's limit for Chat

	chatFieldContent = "reasoning_content"
	chatFieldPlain   = "reasoning"
)

type chatReasoning struct {
	Model string `json:"m"`
	Text  string `json:"t"`
	Field string `json:"f,omitempty"` // the field it came in, when not reasoning_content
}

// EncodeReasoning packs a Chat model's readable reasoning into a Claude thinking signature.
func EncodeReasoning(model, field, text string) string {
	if field == chatFieldContent {
		field = ""
	}
	return chatSignature + chatPack(chatReasoning{model, text, field})
}

// DecodeReasoning unpacks a signature made by EncodeReasoning for the same model.
func DecodeReasoning(sig, model string) (field, text string, ok bool) {
	var r chatReasoning
	if !chatUnpack(sig, chatSignature, &r) || r.Text == "" || r.Model != model {
		return "", "", false
	}
	if r.Field != chatFieldPlain {
		r.Field = chatFieldContent
	}
	return r.Field, r.Text, true
}

type chatCarried struct {
	ID    string          `json:"i"`
	Model string          `json:"m"`
	Extra json.RawMessage `json:"x,omitempty"`
	Nonce string          `json:"n"` // some servers use the same ids in every answer
}

var chatPlainID = regexp.MustCompile(`^[A-Za-z0-9_-]{9,64}$`)

// sealCall makes an id that Claude takes and that brings the server's own back. Most ids stay as they are.
func sealCall(id, model string, extra json.RawMessage) string {
	if len(extra) == 0 && chatPlainID.MatchString(id) && !strings.HasPrefix(id, chatCall) {
		return id
	}
	return chatCall + chatPack(chatCarried{id, model, extra, chatRandom(4)})
}

// openCall returns the id and the opaque state for a server; another model's server gets no state.
func openCall(id, model string) (string, json.RawMessage) {
	var c chatCarried
	if chatUnpack(id, chatCall, &c) && c.ID != "" && c.Model == model {
		return c.ID, c.Extra
	}
	if len(id) <= chatMaxID {
		return id, nil
	}
	return reqShorten(id, reqClean(id), chatMaxID-17, 8), nil
}

var chatShortID = regexp.MustCompile(`^[A-Za-z0-9]{9}$`)

func shortCallID(id string) string {
	if chatShortID.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return strings.ToLower(base32.StdEncoding.EncodeToString(sum[:])[:9])
}

func newCallID() string { return "call_" + chatRandom(12) }

func chatRandom(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func chatPack(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func chatUnpack(s, prefix string, v any) bool {
	body, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	return err == nil && json.Unmarshal(b, v) == nil
}
