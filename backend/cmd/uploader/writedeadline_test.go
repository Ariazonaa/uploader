package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A client that stops reading must not hold the handler forever: its writes
// fail once they have been blocked for the timeout.
func TestWriteDeadlineCutsOffStalledClient(t *testing.T) {
	done := make(chan error, 1)
	srv := httptest.NewServer(withWriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 64<<10)
		for i := 0; i < 1<<14; i++ { // up to 1 GiB, far more than any socket buffers
			if _, err := w.Write(buf); err != nil {
				done <- err
				return
			}
		}
		done <- errors.New("wrote everything to a client that never reads")
	}), 300*time.Millisecond))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")

	select {
	case err := <-done:
		if err == nil || strings.Contains(err.Error(), "never reads") {
			t.Fatalf("handler: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("handler still blocked writing to a client that stopped reading")
	}
}

// A slow client that keeps reading gets the whole file even though the
// transfer takes several times the timeout, including through the ReadFrom
// (sendfile) path http.ServeContent uses.
func TestWriteDeadlineKeepsSlowReaderAlive(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 256<<10) // 4 MiB
	path := filepath.Join(t.TempDir(), "blob")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(withWriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := os.Open(path)
		if err != nil {
			t.Error(err)
			return
		}
		defer f.Close()
		http.ServeContent(w, r, "blob", time.Time{}, f)
	}), 400*time.Millisecond))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got bytes.Buffer
	start := time.Now()
	buf := make([]byte, 64<<10)
	for {
		n, err := resp.Body.Read(buf)
		got.Write(buf[:n])
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("after %d bytes: %v", got.Len(), err)
		}
		time.Sleep(15 * time.Millisecond) // ~4 MiB/s at best
	}
	if !bytes.Equal(got.Bytes(), payload) {
		t.Fatalf("got %d bytes, want %d", got.Len(), len(payload))
	}
	if elapsed := time.Since(start); elapsed < 400*time.Millisecond {
		t.Logf("transfer took only %v; the test did not outlast the timeout", elapsed)
	}
}

// Flushing and the upload paths' SetReadDeadline still reach the connection
// through the wrapper.
func TestWriteDeadlineKeepsResponseControllerWorking(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(withWriteDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(time.Minute)); err != nil {
			t.Errorf("SetReadDeadline through the wrapper: %v", err)
		}
		fmt.Fprintln(w, "first")
		if err := rc.Flush(); err != nil {
			t.Errorf("Flush through the wrapper: %v", err)
		}
		<-release
		fmt.Fprintln(w, "second")
	}), time.Minute))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	defer resp.Body.Close()
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	close(release)
	if err != nil || line != "first\n" {
		t.Fatalf("first line before the handler finished = %q, %v", line, err)
	}
}
