package api

import (
	"context"
	"fmt"
	"testing"

	"uploader/internal/db/store"
)

// Views are aggregated per bucket between flushes; a flush hands each bucket
// over once and leaves nothing pending.
func TestViewBatcherAggregates(t *testing.T) {
	var got []store.ViewDelta
	b := newViewBatcher(func(_ context.Context, d []store.ViewDelta) error {
		got = append(got, d...)
		return nil
	})
	for i := 0; i < 3; i++ {
		b.add("abc", "example.com", "desktop", true)
	}
	b.add("abc", "example.com", "desktop", false) // share page: breakdown only
	b.add("abc", "", "bot", true)

	b.flush(context.Background())
	if len(got) != 2 {
		t.Fatalf("flushed %d buckets, want 2: %+v", len(got), got)
	}
	for _, d := range got {
		switch d.UACategory {
		case "desktop":
			if d.Count != 4 || d.Views != 3 {
				t.Errorf("desktop bucket = %+v, want count 4, views 3", d)
			}
		case "bot":
			if d.Count != 1 || d.Views != 1 {
				t.Errorf("bot bucket = %+v, want 1/1", d)
			}
		}
	}
	got = nil
	b.flush(context.Background())
	if len(got) != 0 {
		t.Fatalf("second flush wrote %d buckets, want none", len(got))
	}
}

// New buckets past the cap are dropped; buckets already pending keep counting.
func TestViewBatcherCapsPendingBuckets(t *testing.T) {
	var got []store.ViewDelta
	b := newViewBatcher(func(_ context.Context, d []store.ViewDelta) error {
		got = append(got, d...)
		return nil
	})
	for i := 0; i < maxPendingViewKeys+50; i++ {
		b.add("abc", fmt.Sprintf("h%d.example", i), "desktop", true)
	}
	b.add("abc", "h0.example", "desktop", true)
	b.flush(context.Background())
	if len(got) != maxPendingViewKeys {
		t.Fatalf("flushed %d buckets, want the cap of %d", len(got), maxPendingViewKeys)
	}
	for _, d := range got {
		if d.ReferrerHost == "h0.example" && d.Count != 2 {
			t.Fatalf("pending bucket stopped counting at the cap: %+v", d)
		}
	}
}
