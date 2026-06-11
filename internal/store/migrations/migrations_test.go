package migrations

import "testing"

func TestKnownListsBothDialectsIdentically(t *testing.T) {
	sq := Known("sqlite")
	pg := Known("postgres")
	if len(sq) == 0 {
		t.Fatal("sqlite migrations empty")
	}
	if len(sq) != len(pg) {
		t.Fatalf("dialect drift: sqlite %v vs postgres %v", sq, pg)
	}
	for i := range sq {
		if sq[i] != pg[i] {
			t.Errorf("name %d: %q != %q", i, sq[i], pg[i])
		}
	}
	if sq[0] != "0001_init" {
		t.Errorf("first migration: got %q want 0001_init", sq[0])
	}
}

func TestKnownUnknownDriverDefaultsToSqlite(t *testing.T) {
	if got := Known("bogus"); len(got) == 0 || got[0] != "0001_init" {
		t.Errorf("default driver listing wrong: %v", got)
	}
}
