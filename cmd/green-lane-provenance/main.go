// green-lane-provenance is for trusted dispatcher/publisher jobs only. It must
// never share a runner or signing key with PR code. It signs observed metadata;
// it does not infer providers from PR bodies, GitHub logins or harness names.
package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/croutoncreations/redline/internal/greenlane"
)

func main() {
	keyID := flag.String("key-id", "", "trusted issuer key ID")
	flag.Parse()
	signed, err := signReceipt(os.Stdin, *keyID, os.Getenv("GREEN_LANE_SIGNING_KEY"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(signed); err != nil {
		fmt.Fprintln(os.Stderr, "cannot write signed receipt")
		os.Exit(1)
	}
}

func signReceipt(input io.Reader, keyID, encoded string) (greenlane.SignedReceipt, error) {
	var empty greenlane.SignedReceipt
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PrivateKeySize || keyID == "" {
		return empty, fmt.Errorf("issuer key ID and base64 Ed25519 private key are required")
	}
	var receipt greenlane.ProvenanceReceipt
	data, err := io.ReadAll(io.LimitReader(input, 65537))
	if err != nil || len(data) > 65536 {
		return empty, fmt.Errorf("receipt input exceeds size limit or cannot be read")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&receipt); err != nil {
		return empty, fmt.Errorf("invalid receipt input")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return empty, fmt.Errorf("trailing receipt input")
	}
	if receipt.Version != 1 || receipt.Repository == "" || receipt.PRNumber <= 0 || receipt.HeadSHA == "" || receipt.BaseSHA == "" || receipt.Provider == "" || receipt.Model == "" || receipt.Harness == "" || receipt.RunID == "" || receipt.IssuedAt.IsZero() || (receipt.Role != "author" && receipt.Role != "reviewer") {
		return empty, fmt.Errorf("incomplete receipt metadata")
	}
	if (receipt.Role == "author" && (receipt.Outcome != "authored" || receipt.ReviewID != 0 || receipt.Reviewer != "")) || (receipt.Role == "reviewer" && (receipt.Outcome != "approved" || receipt.ReviewID <= 0 || receipt.Reviewer == "")) {
		return empty, fmt.Errorf("invalid receipt outcome or review identity")
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return empty, fmt.Errorf("cannot encode receipt")
	}
	return greenlane.SignedReceipt{KeyID: keyID, Payload: payload, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(greenlane.ReceiptDomain), payload...)))}, nil
}
