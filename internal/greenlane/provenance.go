package greenlane

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

const ReceiptDomain = "redline-green-lane-provenance-v1\n"

// strictJSON makes publisher/verifier schema mismatches fail closed.
func strictJSON(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func (e *Evaluator) verifyReceipt(pr PRInfo, signed SignedReceipt) (ProvenanceReceipt, error) {
	var r ProvenanceReceipt
	issuer, ok := e.config.ProvenanceIssuers[signed.KeyID]
	if !ok {
		return r, fmt.Errorf("unknown provenance issuer %q", signed.KeyID)
	}
	public, err := base64.StdEncoding.DecodeString(issuer.PublicKey)
	if err != nil || len(public) != ed25519.PublicKeySize {
		return r, fmt.Errorf("invalid issuer public key")
	}
	signature, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil || issuer.AppID <= 0 || signed.SourceAppID != issuer.AppID || !ed25519.Verify(public, append([]byte(ReceiptDomain), signed.Payload...), signature) {
		return r, fmt.Errorf("invalid signature or publishing App")
	}
	if err := strictJSON(signed.Payload, &r); err != nil {
		return r, fmt.Errorf("invalid receipt schema: %w", err)
	}
	if r.Version != 1 || r.Role != issuer.Role || r.Provider == "" || r.Provider != issuer.Provider || r.Provider != strings.ToLower(strings.TrimSpace(r.Provider)) || r.Model == "" || r.Harness == "" || r.RunID == "" || r.IssuedAt.IsZero() || r.IssuedAt.After(time.Now().UTC().Add(time.Minute)) {
		return r, fmt.Errorf("invalid role, provider or execution metadata")
	}
	if pr.Repository == "" || r.Repository != pr.Repository || r.PRNumber != pr.Number || r.HeadSHA != pr.HeadSHA || r.BaseSHA != pr.BaseSHA {
		return r, fmt.Errorf("receipt does not match repository/PR/head/base")
	}
	if r.Role == "author" {
		if r.Outcome != "authored" || r.ReviewID != 0 || r.Reviewer != "" {
			return r, fmt.Errorf("invalid author outcome")
		}
	} else if r.Role == "reviewer" {
		effective, ok := effectiveReviews(pr.Reviews)[r.Reviewer]
		if r.Outcome != "approved" || issuer.Reviewer == "" || r.Reviewer != issuer.Reviewer || r.Reviewer == pr.Author || r.ReviewID <= 0 || !ok || effective.ID != r.ReviewID || effective.State != "APPROVED" || effective.CommitID != pr.HeadSHA {
			return r, fmt.Errorf("receipt is not bound to an effective independent approval")
		}
	} else {
		return r, fmt.Errorf("unsupported provenance role")
	}
	return r, nil
}

func (e *Evaluator) checkProvenance(pr PRInfo) RuleResult {
	r := RuleResult{Name: "Reviewer", Inconclusive: true}
	if len(e.config.ProvenanceIssuers) == 0 {
		r.Message = "Cross-vendor provenance not verified: no trusted receipt issuers configured"
		return r
	}
	if pr.ProvenanceError != "" {
		r.Message = "Provenance evidence unavailable: " + pr.ProvenanceError
		return r
	}
	var authorProvider string
	var reviewers []ProvenanceReceipt
	for _, signed := range pr.Provenance {
		receipt, err := e.verifyReceipt(pr, signed)
		if err != nil {
			r.Message = "Provenance rejected: " + err.Error()
			return r
		}
		if receipt.Role == "author" {
			if authorProvider != "" && authorProvider != receipt.Provider {
				r.Message = "Conflicting author provider receipts"
				return r
			}
			authorProvider = receipt.Provider
		} else {
			reviewers = append(reviewers, receipt)
		}
	}
	if authorProvider == "" {
		r.Message = "Missing trusted author provenance on current head/base"
		return r
	}
	for _, reviewer := range reviewers {
		if reviewer.Provider != authorProvider {
			r.Pass = true
			r.Inconclusive = false
			r.Message = "OK: signed independent provider review (" + authorProvider + " → " + reviewer.Provider + ") on current head/base"
			return r
		}
	}
	r.Message = "Missing independent-provider approval receipt on current head/base"
	return r
}
