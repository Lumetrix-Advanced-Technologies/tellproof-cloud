package keys_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lumetrix-advanced-technologies/tellproof/internal/jcs"
)

// packPayload is pass vector 1's statement with another issued_at (the subject
// digest is recomputed, so the pack is valid apart from who signs it).
func packPayload(t *testing.T, issuedAt string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "spec", "vectors", "pass", "KST-CTA-0001.dsse.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Payload string `json:"payload"`
	}
	_ = json.Unmarshal(b, &env)
	raw, _ := base64.StdEncoding.DecodeString(env.Payload)
	var st map[string]any
	_ = json.Unmarshal(raw, &st)
	pred := st["predicate"].(map[string]any)
	pred["issued_at"] = issuedAt
	pb, _ := json.Marshal(pred)
	cp, err := jcs.Canonicalize(pb)
	if err != nil {
		t.Fatal(err)
	}
	st["subject"].([]any)[0].(map[string]any)["digest"].(map[string]any)["sha256"] = sha256Hex(cp)
	sb, _ := json.Marshal(st)
	out, err := jcs.Canonicalize(sb)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
