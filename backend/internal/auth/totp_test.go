package auth

import (
	"strings"
	"testing"
)

func TestTOTPRoundTrip(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	// A code computed for the current step must validate.
	code, err := totpAt(secret, currentCounter())
	if err != nil {
		t.Fatal(err)
	}
	if !ValidateTOTP(secret, code) {
		t.Fatalf("expected code %q to validate", code)
	}
	if ValidateTOTP(secret, "000000") && code != "000000" {
		t.Fatalf("a wrong code unexpectedly validated")
	}
	if ValidateTOTP(secret, "12345") {
		t.Fatalf("short code should not validate")
	}
}

func TestEncryptStringRoundTrip(t *testing.T) {
	key := make([]byte, 32)
	enc, err := EncryptString(key, "shh-secret")
	if err != nil {
		t.Fatal(err)
	}
	if enc == "shh-secret" {
		t.Fatal("ciphertext equals plaintext")
	}
	dec, err := DecryptString(key, enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec != "shh-secret" {
		t.Fatalf("got %q, want shh-secret", dec)
	}
	// Wrong key must fail to open.
	bad := make([]byte, 32)
	bad[0] = 1
	if _, err := DecryptString(bad, enc); err == nil {
		t.Fatal("decrypt with wrong key should fail")
	}
}

func TestRecoveryCodes(t *testing.T) {
	plain, hashed, err := GenerateRecoveryCodes(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 3 {
		t.Fatalf("want 3 codes, got %d", len(plain))
	}
	remaining, ok := ConsumeRecoveryCode(hashed, plain[1])
	if !ok {
		t.Fatal("valid recovery code was rejected")
	}
	// The consumed code must no longer work.
	if _, ok := ConsumeRecoveryCode(remaining, plain[1]); ok {
		t.Fatal("recovery code reuse should fail")
	}
	// Another code still works.
	if _, ok := ConsumeRecoveryCode(remaining, plain[0]); !ok {
		t.Fatal("a remaining recovery code should still validate")
	}
}

func TestLooksLikeRecoveryCode(t *testing.T) {
	for _, ok := range []string{"abcd-efgh", "2345-6723", "ABCD-EFGH", "abcdefgh", " abcd efgh ", "ab-cd-ef-gh"} {
		if !LooksLikeRecoveryCode(ok) {
			t.Errorf("LooksLikeRecoveryCode(%q) = false, want true", ok)
		}
	}
	// A six-digit TOTP code must not reach the bcrypt loop: matching walks up to
	// ten hashes, which turned every mistyped code into ~600ms of CPU.
	for _, bad := range []string{"123456", "123 456", "", "abcd_efgh", "abcd-efg", "abcd-efgh1", "ab1d-efgh"} {
		if LooksLikeRecoveryCode(bad) {
			t.Errorf("LooksLikeRecoveryCode(%q) = true, want false", bad)
		}
	}
}

func TestConsumeRecoveryCodeIgnoresNonRecoveryInput(t *testing.T) {
	plain, hashed, err := GenerateRecoveryCodes(3)
	if err != nil {
		t.Fatalf("GenerateRecoveryCodes: %v", err)
	}
	// A TOTP-shaped code leaves the stored hashes untouched and does no hashing.
	if remaining, used := ConsumeRecoveryCode(hashed, "123456"); used || remaining != hashed {
		t.Fatal("a six-digit code was treated as a recovery code")
	}
	// A real code is consumed exactly once.
	remaining, used := ConsumeRecoveryCode(hashed, plain[1])
	if !used {
		t.Fatal("a valid recovery code was rejected")
	}
	if _, reused := ConsumeRecoveryCode(remaining, plain[1]); reused {
		t.Fatal("a recovery code was accepted twice")
	}
}

// Codes are accepted the way people read them off a screen: authenticator apps
// show "123 456", the recovery list "abcd-efgh".
func TestCodesIgnoreSeparators(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	code, err := totpAt(secret, currentCounter())
	if err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{code[:3] + " " + code[3:], code[:3] + "-" + code[3:], " " + code + "\n", code[:2] + " " + code[2:4] + " " + code[4:]} {
		if !ValidateTOTP(secret, typed) {
			t.Errorf("ValidateTOTP(%q) = false, want true", typed)
		}
	}

	plain, hashed, err := GenerateRecoveryCodes(1)
	if err != nil {
		t.Fatal(err)
	}
	bare := plain[0][:4] + plain[0][5:]
	for _, typed := range []string{bare, strings.ToUpper(plain[0]), plain[0][:4] + " " + plain[0][5:], " " + strings.ToUpper(bare) + " "} {
		if _, ok := ConsumeRecoveryCode(hashed, typed); !ok {
			t.Errorf("ConsumeRecoveryCode(%q) for %q = false, want true", typed, plain[0])
		}
	}
}

func TestValidateTOTPStepReportsMatchedStep(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	if err != nil {
		t.Fatalf("GenerateTOTPSecret: %v", err)
	}
	now := currentCounter()
	code, err := totpAt(secret, now)
	if err != nil {
		t.Fatalf("totpAt: %v", err)
	}
	step, ok := ValidateTOTPStep(secret, code)
	if !ok {
		t.Fatal("current code was rejected")
	}
	// The caller persists this step to make the code single-use, so it has to be
	// the step the code actually belongs to.
	if step != now {
		t.Fatalf("step = %d, want %d", step, now)
	}
	// A code from the previous window still validates (clock skew) but reports
	// the older step, which the replay guard will refuse once now is spent.
	prev, err := totpAt(secret, now-1)
	if err != nil {
		t.Fatalf("totpAt: %v", err)
	}
	if step, ok := ValidateTOTPStep(secret, prev); !ok || step != now-1 {
		t.Fatalf("previous-window code: step=%d ok=%v, want step=%d ok=true", step, ok, now-1)
	}
}
