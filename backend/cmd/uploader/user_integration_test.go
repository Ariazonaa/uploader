//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"uploader/internal/db/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

// `user create` bootstraps the admin through the same locked count-and-insert
// as the setup wizard: parallel runs create exactly one account, and a later
// run is refused.
func TestUserCreateBootstrapsOnlyOnce(t *testing.T) {
	pool, dsn := dbtest.Open(t)
	t.Setenv("UPLOADER_DATABASE_URL", dsn)

	const n = 6
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("boot%d", i)
			errs[i] = runUserCreate([]string{"--username", name, "--email", name + "@example.com", "--password", "longenough"})
		}()
	}
	wg.Wait()
	created := 0
	for i, err := range errs {
		switch {
		case err == nil:
			created++
		case !strings.Contains(err.Error(), "already exists"):
			t.Errorf("run %d: %v", i, err)
		}
	}
	var users, admins int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE is_admin) FROM users`).Scan(&users, &admins); err != nil {
		t.Fatal(err)
	}
	if created != 1 || users != 1 || admins != 1 {
		t.Fatalf("created=%d users=%d admins=%d, want 1/1/1", created, users, admins)
	}
	if err := runUserCreate([]string{"--username", "late", "--email", "late@example.com", "--password", "longenough"}); err == nil {
		t.Fatal("a second bootstrap admin was created")
	}
}
