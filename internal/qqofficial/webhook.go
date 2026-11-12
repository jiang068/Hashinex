package qqofficial

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (c *Client) Webhook(w http.ResponseWriter, r *http.Request, maxBytes int64, ingest func(Dispatch) bool) {
	if appID := r.Header.Get("X-Bot-Appid"); appID != "" && appID != c.appID {
		w.Header().Set("Connection", "close")
		http.Error(w, "unexpected AppID", http.StatusForbidden)
		return
	}
	timestamp := r.Header.Get("X-Signature-Timestamp")
	stamp, stampErr := strconv.ParseInt(timestamp, 10, 64)
	signedAt := time.Unix(stamp, 0)
	signature := r.Header.Get("X-Signature-Ed25519")
	var sig [ed25519.SignatureSize]byte
	if len(c.webhookPublicKey) == 0 || stampErr != nil || !validWebhookTime(signedAt) ||
		len(signature) != hex.EncodedLen(len(sig)) {
		w.Header().Set("Connection", "close")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if _, err := hex.Decode(sig[:], []byte(signature)); err != nil {
		w.Header().Set("Connection", "close")
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if r.ContentLength > maxBytes {
		w.Header().Set("Connection", "close")
		http.Error(w, "invalid callback body", http.StatusBadRequest)
		return
	}
	signed, err := io.ReadAll(io.MultiReader(strings.NewReader(timestamp), http.MaxBytesReader(w, r.Body, maxBytes)))
	if err != nil {
		http.Error(w, "invalid callback body", http.StatusBadRequest)
		return
	}
	if !validWebhookTime(signedAt) || !ed25519.Verify(c.webhookPublicKey, signed, sig[:]) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	body := signed[len(timestamp):]
	var d Dispatch
	if json.Unmarshal(body, &d) != nil {
		http.Error(w, "invalid callback payload", http.StatusBadRequest)
		return
	}
	var reply any
	switch d.Op {
	case 13:
		var challenge struct {
			PlainToken string `json:"plain_token"`
			EventTime  string `json:"event_ts"`
		}
		if json.Unmarshal(d.Data, &challenge) != nil || challenge.PlainToken == "" || challenge.EventTime == "" {
			http.Error(w, "invalid verification challenge", http.StatusBadRequest)
			return
		}
		reply = map[string]string{
			"plain_token": challenge.PlainToken,
			"signature":   hex.EncodeToString(ed25519.Sign(c.webhookPrivateKey, []byte(challenge.EventTime+challenge.PlainToken))),
		}
	case 0:
		d.Raw = body
		if !ingest(d) {
			http.Error(w, "callback queue unavailable", http.StatusServiceUnavailable)
			return
		}
		reply = map[string]int{"op": 12}
	default:
		http.Error(w, "unsupported callback opcode", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}

func validWebhookTime(signedAt time.Time) bool {
	const skew = 5 * time.Minute
	delta := time.Since(signedAt)
	return delta >= -skew && delta <= skew
}
