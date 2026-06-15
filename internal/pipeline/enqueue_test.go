package pipeline

import "testing"

// TestTrySend covers the shared panic-safe non-blocking enqueue (D-067 lens):
// an open channel accepts, a full channel drops, a closed channel drops without
// panicking (the send-on-closed-channel case a bare select/default does NOT
// survive), and a nil channel drops.
func TestTrySend(t *testing.T) {
	t.Run("open channel accepts", func(t *testing.T) {
		ch := make(chan Item, 1)
		if !TrySend(ch, Item{RecordID: "a"}) {
			t.Fatal("TrySend on an open, non-full channel must accept")
		}
		got := <-ch
		if got.RecordID != "a" {
			t.Fatalf("got %q want %q", got.RecordID, "a")
		}
	})

	t.Run("full channel drops", func(t *testing.T) {
		ch := make(chan Item, 1)
		ch <- Item{RecordID: "fill"}
		if TrySend(ch, Item{RecordID: "b"}) {
			t.Fatal("TrySend on a full channel must drop (return false)")
		}
	})

	t.Run("closed channel drops without panic", func(t *testing.T) {
		ch := make(chan Item, 1)
		close(ch)
		// A bare `select { case ch <- x: default: }` would panic here.
		if TrySend(ch, Item{RecordID: "c"}) {
			t.Fatal("TrySend on a closed channel must drop (return false)")
		}
	})

	t.Run("nil channel drops", func(t *testing.T) {
		var ch chan Item
		if TrySend(ch, Item{RecordID: "d"}) {
			t.Fatal("TrySend on a nil channel must drop (return false)")
		}
	})
}
