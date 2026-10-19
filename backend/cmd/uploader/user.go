package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"uploader/internal/auth"
	"uploader/internal/db"
	"uploader/internal/db/store"
)

const userUsage = `usage: uploader user create --username U --email E --password P
       uploader user disable-2fa --username U`

func runUser(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "create":
			return runUserCreate(args[1:])
		case "disable-2fa":
			return runUserDisable2FA(args[1:])
		}
	}
	return errors.New(userUsage)
}

func runUserCreate(args []string) error {
	fs := flag.NewFlagSet("user create", flag.ContinueOnError)
	username := fs.String("username", "", "username for the account")
	email := fs.String("email", "", "email for the account")
	password := fs.String("password", "", "password (or set UPLOADER_SEED_PASSWORD)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *password == "" {
		*password = os.Getenv("UPLOADER_SEED_PASSWORD")
	}
	// Trimmed like setup and registration do; login trims the name it looks up.
	*username = strings.TrimSpace(*username)
	*email = strings.TrimSpace(*email)
	if *username == "" || *email == "" || *password == "" {
		return errors.New("--username, --email and --password are all required")
	}
	if err := auth.ValidatePassword(*password); err != nil {
		return err
	}

	ctx := context.Background()
	pool, err := openUserDB(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)

	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}
	// Bootstrap admin: the CLI creates the first (admin) account. Further users
	// join via invite registration; additional admins are promoted in the DB.
	// CreateFirstAdmin counts and inserts under the advisory lock the setup
	// wizard takes too; a separate count, then insert, let a CLI run and a
	// browser on /auth/setup both create an admin.
	created, err := st.CreateFirstAdmin(ctx, store.User{
		Username:     *username,
		Email:        *email,
		PasswordHash: hash,
	})
	if err != nil {
		return err
	}
	if !created {
		return errors.New("an account already exists; the CLI only creates the bootstrap admin")
	}
	fmt.Printf("created admin user %q\n", *username)
	return nil
}

// runUserDisable2FA turns 2FA off for one account. It is the way back in for
// someone who lost both their authenticator and their recovery codes, or whose
// TOTP secret no longer decrypts (it is encrypted with the session block key),
// and it needs shell access to the server, not a web login.
func runUserDisable2FA(args []string) error {
	fs := flag.NewFlagSet("user disable-2fa", flag.ContinueOnError)
	username := fs.String("username", "", "account to disable 2FA for")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *username == "" {
		return errors.New("--username is required")
	}

	ctx := context.Background()
	pool, err := openUserDB(ctx)
	if err != nil {
		return err
	}
	defer pool.Close()
	st := store.New(pool)

	u, err := st.GetUserByUsername(ctx, *username)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no account named %q", *username)
	}
	if err != nil {
		return err
	}
	if !u.TOTPEnabled && u.TOTPSecret == nil {
		fmt.Printf("2FA is not enabled for %q, nothing to do\n", u.Username)
		return nil
	}
	if err := st.DisableTOTP(ctx, u.ID); err != nil {
		return err
	}
	fmt.Printf("disabled 2FA for %q; the account now signs in with its password alone\n", u.Username)
	return nil
}

// openUserDB migrates the database and connects to it for a user subcommand.
func openUserDB(ctx context.Context) (*pgxpool.Pool, error) {
	dbURL := os.Getenv("UPLOADER_DATABASE_URL")
	if dbURL == "" {
		return nil, errors.New("UPLOADER_DATABASE_URL is required")
	}
	if err := db.Migrate(ctx, dbURL, "up"); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db.Connect(ctx, dbURL)
}
