package jobs

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
)

// A flood of dropped jobs produces one log line right away and one summary on
// shutdown, not a line per drop.
func TestDroppedJobsAreReportedInBulk(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	q := New("test", 1, 1)
	running, release := make(chan struct{}), make(chan struct{})
	q.Submit(func(context.Context) { close(running); <-release })
	<-running
	if !q.Submit(func(context.Context) {}) {
		t.Fatal("the buffered slot was not free")
	}
	for i := 0; i < 1000; i++ {
		if q.Submit(func(context.Context) {}) {
			t.Fatal("a full queue accepted a job")
		}
	}
	close(release)
	q.Shutdown()

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d log lines for 1000 drops, want 2:\n%s", len(lines), buf.String())
	}
	if !strings.Contains(lines[0], "test queue full, dropped 1 job(s)") ||
		!strings.Contains(lines[1], "dropped 999 job(s)") {
		t.Fatalf("unexpected report:\n%s", buf.String())
	}
}
