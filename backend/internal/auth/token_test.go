package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func TestGenerateTokenAndVerify(t *testing.T) {
	gen, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(gen.Plaintext, "upl_") {
		t.Errorf("token %q lacks scheme prefix", gen.Plaintext)
	}
	// 32 random bytes in base64url; what follows the stored prefix must still
	// carry well over 192 bits.
	if secret := len(gen.Plaintext) - TokenPrefixLen; secret*6 < 192 {
		t.Errorf("only %d bits of the token are secret", secret*6)
	}
	if !strings.HasPrefix(gen.Hash, "sha256:") || IsLegacyTokenHash(gen.Hash) {
		t.Errorf("new token hash %q is not tagged SHA-256", gen.Hash)
	}
	if len(gen.Prefix) != TokenPrefixLen {
		t.Errorf("prefix length = %d, want %d", len(gen.Prefix), TokenPrefixLen)
	}
	if gen.Prefix != gen.Plaintext[:TokenPrefixLen] {
		t.Error("prefix is not a prefix of the plaintext")
	}
	if !VerifyToken(gen.Hash, gen.Plaintext) {
		t.Error("VerifyToken rejected the correct token")
	}
	if VerifyToken(gen.Hash, gen.Plaintext+"x") {
		t.Error("VerifyToken accepted a wrong token")
	}
}

// Tokens minted before the switch to SHA-256 hold a bcrypt hash and keep
// working; a stored value of any other shape never matches.
func TestVerifyTokenHashFormats(t *testing.T) {
	const plain = "upl_legacyTokenValue0123456789abcd"
	legacy, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if !IsLegacyTokenHash(string(legacy)) {
		t.Fatalf("bcrypt hash %q not recognised as legacy", legacy)
	}
	if !VerifyToken(string(legacy), plain) {
		t.Error("legacy bcrypt token rejected")
	}
	if VerifyToken(string(legacy), plain+"x") {
		t.Error("legacy bcrypt hash accepted a wrong token")
	}
	for _, stored := range []string{plain, "", "sha256:", "sha256:" + plain, "md5:abc"} {
		if VerifyToken(stored, plain) {
			t.Errorf("stored value %q verified", stored)
		}
	}
	sum := sha256.Sum256([]byte(plain))
	if got, want := HashToken(plain), "sha256:"+hex.EncodeToString(sum[:]); got != want {
		t.Errorf("HashToken = %q, want %q", got, want)
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		g, err := GenerateToken()
		if err != nil {
			t.Fatal(err)
		}
		if seen[g.Plaintext] {
			t.Fatal("duplicate token generated")
		}
		seen[g.Plaintext] = true
	}
}

func TestLooksLikeToken(t *testing.T) {
	if !LooksLikeToken("upl_abc") {
		t.Error("upl_ prefix should look like a token")
	}
	if LooksLikeToken("Bearer something") {
		t.Error("non-scheme string should not look like a token")
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Error("VerifyPassword rejected the correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Error("VerifyPassword accepted a wrong password")
	}
}
