package greenlane

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// Details and receipt text are base64 in the digest's wire format, avoiding
// differences between Go and JavaScript JSON string escaping.
type CheckEvidence struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Head       string `json:"head"`
	AppID      int64  `json:"app"`
	Details    string `json:"details"`
	Text       string `json:"text"`
}

func NewCheckEvidence(id int64, name, status, conclusion, head string, app int64, details, text string) CheckEvidence {
	return CheckEvidence{id, name, status, conclusion, head, app, base64.StdEncoding.EncodeToString([]byte(details)), base64.StdEncoding.EncodeToString([]byte(text))}
}
func CheckSnapshot(evidence []CheckEvidence) string {
	rows := append([]CheckEvidence{}, evidence...)
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	data, _ := json.Marshal(rows)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
