//go:build integration

package store_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"uploader/internal/db/store"
)

func newUser(name string) store.User {
	return store.User{Username: name, Email: name + "@example.com", PasswordHash: "x"}
}

func TestRegisterWithInviteValidCode(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin", true)
	if err := e.st.CreateInvite(e.ctx, "good", admin.ID, nil); err != nil {
		t.Fatal(err)
	}
	u, err := e.st.RegisterWithInvite(e.ctx, newUser("alice"), "good")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	got, err := e.st.GetUserByUsername(e.ctx, "alice")
	if err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if got.ID != u.ID || got.IsAdmin {
		t.Fatalf("created user = %+v, want id %s and non-admin", got, u.ID)
	}
	invites, err := e.st.ListInvites(e.ctx)
	if err != nil || len(invites) != 1 {
		t.Fatalf("list invites = %v, %v", invites, err)
	}
	inv := invites[0]
	if inv.UsedBy == nil || *inv.UsedBy != u.ID || inv.UsedAt == nil {
		t.Fatalf("invite not marked used: %+v", inv)
	}
	// A used code is kept as audit trail and cannot be revoked any more.
	if err := e.st.RevokeInvite(e.ctx, "good"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke used invite = %v, want ErrNotFound", err)
	}
}

// Deleting the account that redeemed a code clears used_by (ON DELETE SET
// NULL). The code must stay used: not redeemable again, not revocable, and
// still listed as used.
func TestInviteStaysUsedAfterTheInvitedAccountIsDeleted(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin", true)
	if err := e.st.CreateInvite(e.ctx, "once", admin.ID, nil); err != nil {
		t.Fatal(err)
	}
	u, err := e.st.RegisterWithInvite(e.ctx, newUser("leaver"), "once")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM invite_codes WHERE code = 'once' AND used_by IS NULL AND used_at IS NOT NULL`); n != 1 {
		t.Fatal("precondition: deleting the account did not clear used_by")
	}

	if _, err := e.st.RegisterWithInvite(e.ctx, newUser("second"), "once"); !errors.Is(err, store.ErrInvalidInvite) {
		t.Fatalf("second redemption = %v, want ErrInvalidInvite", err)
	}
	if n := e.count(`SELECT count(*) FROM users WHERE username = 'second'`); n != 0 {
		t.Fatal("second redemption created an account")
	}
	if err := e.st.RevokeInvite(e.ctx, "once"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("revoke = %v, want ErrNotFound (used codes are kept)", err)
	}
	invites, err := e.st.ListInvites(e.ctx)
	if err != nil || len(invites) != 1 || invites[0].UsedAt == nil {
		t.Fatalf("list = %+v, %v; want the code with used_at set", invites, err)
	}
}

func TestRegisterWithInviteRejectsBadCodes(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin", true)
	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	if err := e.st.CreateInvite(e.ctx, "expired", admin.ID, &past); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CreateInvite(e.ctx, "once", admin.ID, &future); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.RegisterWithInvite(e.ctx, newUser("first"), "once"); err != nil {
		t.Fatalf("first use of a valid code: %v", err)
	}

	for _, tc := range []struct{ name, code, user string }{
		{"reused", "once", "second"},
		{"unknown", "nope", "third"},
		{"expired", "expired", "fourth"},
		{"empty", "", "fifth"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.st.RegisterWithInvite(e.ctx, newUser(tc.user), tc.code)
			if !errors.Is(err, store.ErrInvalidInvite) {
				t.Fatalf("err = %v, want ErrInvalidInvite", err)
			}
			if n := e.count(`SELECT count(*) FROM users WHERE username = $1`, tc.user); n != 0 {
				t.Fatalf("%d user row(s) left behind for %s", n, tc.user)
			}
		})
	}
	if n := e.count(`SELECT count(*) FROM invite_codes WHERE code = 'expired' AND used_by IS NULL`); n != 1 {
		t.Fatal("expired invite was claimed")
	}
}

// A registration that fails on the unique username/e-mail must roll back the
// claim, so the invite can still be used for another name.
func TestRegisterWithInviteDuplicateDoesNotBurnCode(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin", true)
	e.user("alice", false)
	if err := e.st.CreateInvite(e.ctx, "code", admin.ID, nil); err != nil {
		t.Fatal(err)
	}

	for _, dup := range []store.User{
		{Username: "ALICE", Email: "other@example.com", PasswordHash: "x"}, // case-insensitive username
		{Username: "bob", Email: "Alice@Example.com", PasswordHash: "x"},   // case-insensitive e-mail
	} {
		_, err := e.st.RegisterWithInvite(e.ctx, dup, "code")
		if sqlState(err) != "23505" {
			t.Fatalf("duplicate %s/%s: err = %v, want unique violation (23505)", dup.Username, dup.Email, err)
		}
	}
	if n := e.count(`SELECT count(*) FROM invite_codes WHERE code = 'code' AND used_by IS NULL AND used_at IS NULL`); n != 1 {
		t.Fatal("failed registration burned the invite code")
	}
	if n := e.count(`SELECT count(*) FROM users`); n != 2 {
		t.Fatalf("users = %d, want 2 (no row left by the failed attempts)", n)
	}
	if _, err := e.st.RegisterWithInvite(e.ctx, newUser("carol"), "code"); err != nil {
		t.Fatalf("code unusable after a failed attempt: %v", err)
	}
}

func TestRegisterWithInviteConcurrentClaims(t *testing.T) {
	e := newEnv(t)
	admin := e.user("admin", true)
	if err := e.st.CreateInvite(e.ctx, "race", admin.ID, nil); err != nil {
		t.Fatal(err)
	}
	const n = 12
	errs := parallel(n, func(i int) error {
		_, err := e.st.RegisterWithInvite(e.ctx, newUser(fmt.Sprintf("racer%d", i)), "race")
		return err
	})
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrInvalidInvite):
		default:
			t.Errorf("racer%d: unexpected error %v", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d registrations succeeded with one code, want exactly 1", ok)
	}
	if got := e.count(`SELECT count(*) FROM users WHERE username LIKE 'racer%'`); got != 1 {
		t.Fatalf("%d racer users exist, want 1", got)
	}
	if got := e.count(`SELECT count(*) FROM invite_codes i JOIN users u ON u.id = i.used_by WHERE i.code = 'race'`); got != 1 {
		t.Fatal("invite does not point at the winning user")
	}
}

func TestCreateFirstAdminConcurrent(t *testing.T) {
	e := newEnv(t)
	const n = 12
	created := make([]bool, n)
	errs := parallel(n, func(i int) error {
		u := newUser(fmt.Sprintf("boot%d", i))
		var err error
		created[i], err = e.st.CreateFirstAdmin(e.ctx, u)
		return err
	})
	wins := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("boot%d: %v", i, err)
		}
		if created[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d bootstrap calls reported success, want 1", wins)
	}
	users, _ := e.st.CountUsers(e.ctx)
	admins := e.count(`SELECT count(*) FROM users WHERE is_admin`)
	if users != 1 || admins != 1 {
		t.Fatalf("users=%d admins=%d, want 1/1", users, admins)
	}
	// Once any account exists the bootstrap is a no-op.
	if ok, err := e.st.CreateFirstAdmin(e.ctx, newUser("late")); ok || err != nil {
		t.Fatalf("late bootstrap = %v, %v; want false, nil", ok, err)
	}
}

func TestReplaceTOTPRecoveryCompareAndSwap(t *testing.T) {
	e := newEnv(t)
	u := e.user("totp", false)
	const full = "h1\nh2\nh3"
	if _, err := e.st.SetTOTPSecret(e.ctx, u.ID, "enc"); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.st.EnableTOTP(e.ctx, u.ID, "enc", full); err != nil || !ok {
		t.Fatalf("enable = %v, %v", ok, err)
	}

	// Stale "old" value: nothing changes.
	ok, err := e.st.ReplaceTOTPRecovery(e.ctx, u.ID, "h1\nh2", "h2")
	if err != nil || ok {
		t.Fatalf("stale swap = %v, %v; want false, nil", ok, err)
	}
	// Unknown user: no swap.
	if ok, err := e.st.ReplaceTOTPRecovery(e.ctx, uuid.New(), full, "h2\nh3"); err != nil || ok {
		t.Fatalf("swap on missing user = %v, %v", ok, err)
	}

	// Parallel redemption of h1: exactly one request may consume it.
	const n = 12
	swapped := make([]bool, n)
	errs := parallel(n, func(i int) error {
		var err error
		swapped[i], err = e.st.ReplaceTOTPRecovery(e.ctx, u.ID, full, "h2\nh3")
		return err
	})
	wins := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("redeem %d: %v", i, err)
		}
		if swapped[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d parallel redemptions succeeded, want 1", wins)
	}
	got, err := e.st.GetUserByID(e.ctx, u.ID)
	if err != nil || got.TOTPRecovery != "h2\nh3" {
		t.Fatalf("recovery = %q, %v; want %q", got.TOTPRecovery, err, "h2\nh3")
	}
	// A second, different code can then be redeemed against the new value, and
	// the old snapshot can no longer resurrect h1.
	if ok, _ := e.st.ReplaceTOTPRecovery(e.ctx, u.ID, "h2\nh3", "h3"); !ok {
		t.Fatal("sequential redemption failed")
	}
	if ok, _ := e.st.ReplaceTOTPRecovery(e.ctx, u.ID, full, "h2\nh3"); ok {
		t.Fatal("stale snapshot overwrote a newer recovery list")
	}
}

// Enrolment writes are conditional: a setup must not touch an enabled 2FA, and
// enabling only succeeds against the secret the code was checked with.
func TestTOTPEnrollmentIsConditional(t *testing.T) {
	e := newEnv(t)
	u := e.user("enrol", false)
	if ok, err := e.st.SetTOTPSecret(e.ctx, u.ID, "first"); err != nil || !ok {
		t.Fatalf("set first secret = %v, %v", ok, err)
	}
	// A second tab started over: the secret the first tab verified is gone.
	if ok, err := e.st.SetTOTPSecret(e.ctx, u.ID, "second"); err != nil || !ok {
		t.Fatalf("replace pending secret = %v, %v", ok, err)
	}
	if ok, err := e.st.EnableTOTP(e.ctx, u.ID, "first", "r1"); err != nil || ok {
		t.Fatalf("enable with a replaced secret = %v, %v; want false", ok, err)
	}
	if ok, err := e.st.EnableTOTP(e.ctx, u.ID, "second", "r2"); err != nil || !ok {
		t.Fatalf("enable with the current secret = %v, %v", ok, err)
	}
	// Once enabled, neither a late setup nor a repeated enable changes anything.
	if ok, err := e.st.SetTOTPSecret(e.ctx, u.ID, "third"); err != nil || ok {
		t.Fatalf("setup over enabled 2FA = %v, %v; want false", ok, err)
	}
	if ok, err := e.st.EnableTOTP(e.ctx, u.ID, "second", "r3"); err != nil || ok {
		t.Fatalf("second enable = %v, %v; want false", ok, err)
	}
	got, err := e.st.GetUserByID(e.ctx, u.ID)
	if err != nil || !got.TOTPEnabled || got.TOTPSecret == nil || *got.TOTPSecret != "second" || got.TOTPRecovery != "r2" {
		t.Fatalf("user after the races = %+v, %v; want enabled with secret second and recovery r2", got, err)
	}
	if ok, err := e.st.SetTOTPSecret(e.ctx, uuid.New(), "x"); err != nil || ok {
		t.Fatalf("setup for a missing user = %v, %v", ok, err)
	}
}

func TestClaimTOTPStepSingleUse(t *testing.T) {
	e := newEnv(t)
	u := e.user("step", false)
	const n = 12
	won := make([]bool, n)
	errs := parallel(n, func(i int) error {
		var err error
		won[i], err = e.st.ClaimTOTPStep(e.ctx, u.ID, 1000)
		return err
	})
	wins := 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("claim %d: %v", i, err)
		}
		if won[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("step claimed %d times, want 1", wins)
	}
	if ok, _ := e.st.ClaimTOTPStep(e.ctx, u.ID, 999); ok {
		t.Fatal("older step accepted after a newer one")
	}
	if ok, _ := e.st.ClaimTOTPStep(e.ctx, u.ID, 1001); !ok {
		t.Fatal("newer step refused")
	}
	// Disabling 2FA resets the guard.
	if err := e.st.DisableTOTP(e.ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.GetUserByID(e.ctx, u.ID); got.TOTPLastStep != 0 || got.TOTPEnabled || got.TOTPSecret != nil {
		t.Fatalf("DisableTOTP left state behind: %+v", got)
	}
}

func TestDeleteUserUnlessLastAdmin(t *testing.T) {
	e := newEnv(t)
	root := e.user("root", true)
	plain := e.user("plain", false)

	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, root.ID); !errors.Is(err, store.ErrLastAdmin) {
		t.Fatalf("deleting the only admin = %v, want ErrLastAdmin", err)
	}
	if _, err := e.st.GetUserByID(e.ctx, root.ID); err != nil {
		t.Fatalf("last admin was deleted: %v", err)
	}
	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, plain.ID); err != nil {
		t.Fatalf("deleting a non-admin: %v", err)
	}
	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, plain.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleting a missing user = %v, want ErrNotFound", err)
	}
	// A non-admin can always delete themselves, even with no admin left.
	e.exec(`UPDATE users SET is_admin = false WHERE id = $1`, root.ID)
	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, root.ID); err != nil {
		t.Fatalf("deleting a non-admin with zero admins: %v", err)
	}
}

func TestDeleteUserUnlessLastAdminConcurrent(t *testing.T) {
	for round := 0; round < 5; round++ {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			e := newEnv(t)
			const n = 4
			ids := make([]uuid.UUID, n)
			for i := range ids {
				ids[i] = e.user(fmt.Sprintf("adm%d", i), true).ID
			}
			errs := parallel(n, func(i int) error { return e.st.DeleteUserUnlessLastAdmin(e.ctx, ids[i]) })
			deleted, refused := 0, 0
			for i, err := range errs {
				switch {
				case err == nil:
					deleted++
				case errors.Is(err, store.ErrLastAdmin):
					refused++
				default:
					t.Errorf("adm%d: %v", i, err)
				}
			}
			admins := e.count(`SELECT count(*) FROM users WHERE is_admin`)
			if admins < 1 {
				t.Fatal("concurrent self-deletes removed every admin")
			}
			if deleted != n-1 || refused != 1 || admins != 1 {
				t.Fatalf("deleted=%d refused=%d admins=%d, want %d/1/1", deleted, refused, admins, n-1)
			}
		})
	}
}

// Deleting an account cascades to its rows; the file-row trigger from 0013
// also removes the view stats of the deleted user's files.
func TestDeleteUserCascades(t *testing.T) {
	e := newEnv(t)
	e.user("keeper", true)
	u := e.user("leaver", false)
	f := e.file(u.ID, "leaverfile")
	if err := e.st.RecordView(e.ctx, f.Slug, "example.com", "desktop"); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO sessions (id, user_id, expires_at) VALUES ('\x01', $1, now() + interval '1 day')`, u.ID)
	if err := e.st.SavePushSubscription(e.ctx, store.PushSubscription{UserID: u.ID, Endpoint: "https://push.example/1", P256dh: "p", Auth: "a"}, 20); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DeleteUserUnlessLastAdmin(e.ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	for table, q := range map[string]string{
		"files":              `SELECT count(*) FROM files WHERE user_id = $1`,
		"sessions":           `SELECT count(*) FROM sessions WHERE user_id = $1`,
		"push_subscriptions": `SELECT count(*) FROM push_subscriptions WHERE user_id = $1`,
	} {
		if n := e.count(q, u.ID); n != 0 {
			t.Errorf("%s: %d row(s) survived the account deletion", table, n)
		}
	}
	if n := e.count(`SELECT count(*) FROM file_view_stats WHERE slug = 'leaverfile'`); n != 0 {
		t.Errorf("view stats survived the account deletion")
	}
}
