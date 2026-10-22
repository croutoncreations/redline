package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/croutoncreations/redline/internal/greenlane"
)

const authorInput = `{"version":1,"role":"author","repository":"owner/repo","pr_number":1,"head_sha":"head","base_sha":"base","provider":"anthropic","model":"model","harness":"trusted","run_id":"run-1","issued_at":"2026-10-21T00:00:00Z","outcome":"authored"}`

func TestSignerPreservesSignatureThroughJSONFormatting(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(key)
	signed, err := signReceipt(strings.NewReader(authorInput), "author-key", encoded)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent([]greenlane.SignedReceipt{signed}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), encoded) {
		t.Fatal("private key leaked into receipt")
	}
	var roundtrip []greenlane.SignedReceipt
	if err := json.Unmarshal(data, &roundtrip); err != nil {
		t.Fatal(err)
	}
	signature, err := base64.StdEncoding.DecodeString(roundtrip[0].Signature)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(pub, append([]byte(greenlane.ReceiptDomain), roundtrip[0].Payload...), signature) {
		t.Fatal("signature did not survive transport")
	}
}
func TestSignerRejectsIncompleteOrUnstructuredMetadata(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	encoded := base64.StdEncoding.EncodeToString(key)
	for _, input := range []string{`{}`, authorInput + ` {}`, strings.Replace(authorInput, `"outcome":"authored"`, `"outcome":"approved"`, 1), strings.Replace(authorInput, `"version":1`, `"version":2`, 1), strings.Replace(authorInput, `"version":1`, `"untrusted_extra":true,"version":1`, 1)} {
		if _, err := signReceipt(strings.NewReader(input), "key", encoded); err == nil {
			t.Fatal("invalid metadata signed")
		}
	}
	if _, err := signReceipt(strings.NewReader(authorInput), "key", "sensitive-invalid-key"); err == nil || strings.Contains(err.Error(), "sensitive-invalid-key") {
		t.Fatal("invalid key accepted or disclosed")
	}
}
