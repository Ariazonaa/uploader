// Package dbtest provisions throwaway PostgreSQL databases for the
// integration tests (build tag "integration"). Without that tag the package is
// empty; see dbtest.go.
//
// Usage from a test package:
//
//	func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
//
//	func TestSomething(t *testing.T) {
//		pool, dsn := dbtest.Open(t) // fresh, fully migrated database
//		...
//	}
//
// The server is taken from UPLOADER_TEST_DATABASE_URL; when it is unset every
// test that calls Open is skipped. The role in that URL must be allowed to
// CREATE DATABASE. Never point it at a database whose data you care about:
// only databases this package created are dropped, but it is still a test
// harness running DDL against that server.
package dbtest
