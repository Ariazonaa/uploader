package auth

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testSessionManager() *SessionManager {
	return NewSessionManager(nil, bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32), time.Hour, false)
}

func TestPendingTokenRoundTrip(t *testing.T) {
	m := testSessionManager()
	id := uuid.New()
	tok, err := m.IssuePending(id, "hash")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.VerifyPending(tok)
	if err != nil || p.UserID != id || p.Key == "" {
		t.Fatalf("VerifyPending = %+v, %v; want user %s with a key", p, err, id)
	}
	if _, err := m.VerifyPending(tok[:len(tok)-4] + "AAAA"); err == nil {
		t.Fatal("tampered token accepted")
	}
	if _, err := testSessionManager().VerifyPending(tok); err != nil {
		t.Fatalf("same keys, other manager: %v", err)
	}
}

func TestPendingTokenIsBoundToThePassword(t *testing.T) {
	m := testSessionManager()
	tok, err := m.IssuePending(uuid.New(), "$2a$10$old")
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.VerifyPending(tok)
	if err != nil {
		t.Fatal(err)
	}
	if !p.MatchesPassword("$2a$10$old") {
		t.Fatal("token does not match the hash it was issued for")
	}
	if p.MatchesPassword("$2a$10$new") || p.MatchesPassword("") {
		t.Fatal("token matches another password hash")
	}
	if (Pending{}).MatchesPassword("") {
		t.Fatal("a token without a tag matches")
	}
}

// Every spelling of one token must count as that token, or each re-encoding
// would get a fresh set of attempts.
func TestPendingKeyIgnoresTransportEncoding(t *testing.T) {
	m := testSessionManager()
	id := uuid.New()
	tok, err := m.IssuePending(id, "hash")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := m.VerifyPending(tok)
	respelled := tok[:10] + "\r\n" + tok[10:20] + "\n" + tok[20:]
	q, err := m.VerifyPending(respelled)
	if err != nil {
		t.Fatalf("precondition: base64 with line breaks no longer decodes (%v); the test needs another spelling", err)
	}
	if q.Key != p.Key {
		t.Fatal("a re-encoded token got its own key")
	}
	// Two logins of the same user in the same second are distinct tokens.
	tok2, err := m.IssuePending(id, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if p2, _ := m.VerifyPending(tok2); p2.Key == p.Key {
		t.Fatal("two pending tokens share a key")
	}
}
