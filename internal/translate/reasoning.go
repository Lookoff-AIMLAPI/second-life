package translate

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

const reqSignaturePrefix = "c2c1."

type reqSignature struct {
	Model     string `json:"m"`
	Encrypted string `json:"e"`
}

// EncodeSignature packs upstream encrypted reasoning into a Claude thinking signature.
func EncodeSignature(model, enc string) string {
	b, err := json.Marshal(reqSignature{Model: model, Encrypted: enc})
	if err != nil {
		return "" // two plain strings always marshal
	}
	return reqSignaturePrefix + base64.RawURLEncoding.EncodeToString(b)
}

// DecodeSignature unpacks a signature made by EncodeSignature for the same model.
func DecodeSignature(sig, model string) (string, bool) {
	body, ok := strings.CutPrefix(sig, reqSignaturePrefix)
	if !ok {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", false
	}
	var s reqSignature
	if json.Unmarshal(b, &s) != nil || s.Encrypted == "" || s.Model != model {
		return "", false
	}
	return s.Encrypted, true
}
