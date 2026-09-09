package persist

import "testing"

type fakeWriter struct{}

func (fakeWriter) Write(Record) error { return nil }

func TestWriterContract(t *testing.T) {
	var _ Writer = fakeWriter{}
	rec := Record{Tick: 3}
	if rec.Tick != 3 {
		t.Errorf("Record.Tick = %d; want 3", rec.Tick)
	}
}
