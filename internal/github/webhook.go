package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// SignatureHeader is the header GitHub signs the webhook body with.
const SignatureHeader = "X-Hub-Signature-256"

// DeliveryHeader uniquely identifies a webhook delivery. It is the dedup key:
// an old implementation stores it and never checks it, so a captured signed request can
// be replayed into a re-deploy.
const DeliveryHeader = "X-GitHub-Delivery"

// EventHeader names the webhook event.
const EventHeader = "X-GitHub-Event"

// Sign produces the X-Hub-Signature-256 value GitHub sends for body under
// secret. It is the counterpart of VerifySignature, used by the tests and by
// the webhook self-test.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// VerifySignature reports whether header is a valid HMAC-SHA256 of body under
// secret. It runs over the raw body, before any parsing, and compares with
// hmac.Equal — a byte-by-byte == leaks the signature through timing.
func VerifySignature(secret string, body []byte, header string) bool {
	hexDigest, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}

	got, err := hex.DecodeString(hexDigest)
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)

	return hmac.Equal(got, mac.Sum(nil))
}

// PushEvent is everything rec-deploy keeps from a push. The raw payload is never
// persisted: an old implementation stores it forever, for no value and unbounded growth.
type PushEvent struct {
	Ref        string
	SHA        string
	Message    string
	Author     string
	Repository string
}

// Branch returns the pushed branch, or "" when the ref is not a branch — a tag
// push must not deploy whatever branch happens to be checked out.
func (e PushEvent) Branch() string {
	branch, ok := strings.CutPrefix(e.Ref, "refs/heads/")
	if !ok {
		return ""
	}

	return branch
}

// ParsePush extracts the fields rec-deploy keeps from a push payload.
func ParsePush(body []byte) (PushEvent, error) {
	var payload struct {
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		HeadCommit *struct {
			ID      string `json:"id"`
			Message string `json:"message"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"head_commit"`
	}

	if err := json.Unmarshal(body, &payload); err != nil {
		return PushEvent{}, fmt.Errorf("parse push payload: %w", err)
	}

	ev := PushEvent{Ref: payload.Ref, Repository: payload.Repository.FullName}
	if payload.HeadCommit != nil {
		ev.SHA = payload.HeadCommit.ID
		ev.Message = payload.HeadCommit.Message
		ev.Author = payload.HeadCommit.Author.Name
	}

	return ev, nil
}
