package translate

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	reqNameMax  = 64
	reqNameKeep = 55 // room for "_" and 8 hash characters
)

// reqToolName returns name if the upstream accepts it, otherwise a deterministic short form.
func reqToolName(name string) string {
	clean := reqClean(name)
	if clean == name && len(name) <= reqNameMax {
		return name
	}
	return reqShorten(name, clean, reqNameKeep, 4)
}

// reqCallID keeps a call id within the upstream limit. The hash is long: ids of one chat often share a prefix.
func reqCallID(id string) string {
	if len(id) <= reqNameMax {
		return id
	}
	return reqShorten(id, reqClean(id), 47, 8)
}

func reqClean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		}
		return '_'
	}, s)
}

func reqShorten(original, clean string, keep, hashBytes int) string {
	if len(clean) > keep {
		clean = clean[:keep]
	}
	sum := sha256.Sum256([]byte(original))
	return clean + "_" + hex.EncodeToString(sum[:hashBytes])
}
