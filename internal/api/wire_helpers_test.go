package api

// White-box unit tests for the nil-guard branches of the wire mappers.

import "testing"

func TestBreakdownToWire_Nil(t *testing.T) {
	t.Parallel()
	if got := breakdownToWire(nil); got != nil {
		t.Errorf("breakdownToWire(nil) = %v, want nil", got)
	}
}

func TestMemoryToJSON_Nil(t *testing.T) {
	t.Parallel()
	if got := memoryToJSON(nil); got != (memoryJSON{}) {
		t.Errorf("memoryToJSON(nil) = %+v, want zero value", got)
	}
}
