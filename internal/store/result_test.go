package store

import (
	"errors"
	"fmt"
	"testing"
)

type fakeResult struct {
	n   int64
	err error
}

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.n, r.err }

func TestAffectedOne(t *testing.T) {
	none, execErr, countErr := errors.New("none"), errors.New("exec"), errors.New("count")
	if err := AffectedOne(fakeResult{n: 1}, nil, none); err != nil {
		t.Fatal(err)
	}
	if err := AffectedOne(fakeResult{n: 0}, nil, none); err != none {
		t.Fatalf("0 rows: %v", err)
	}
	if err := AffectedOne(nil, execErr, none); err != execErr {
		t.Fatalf("exec error: %v", err)
	}
	if err := AffectedOne(fakeResult{err: countErr}, nil, none); err != countErr {
		t.Fatalf("RowsAffected error must not be dropped: %v", err)
	}
}

type codedError int

func (e codedError) Error() string { return "coded" }
func (e codedError) Code() int     { return int(e) }

func TestBusy(t *testing.T) {
	for code, want := range map[int]bool{5: true, 6: true, 5 | 2<<8: true, 6 | 1<<8: true, 1: false, 19: false, 2067: false} {
		if got := Busy(fmt.Errorf("wrapped: %w", codedError(code))); got != want {
			t.Errorf("Busy(code %d) = %v", code, got)
		}
	}
	if Busy(errors.New("database is locked")) || Busy(nil) {
		t.Fatal("errors without a SQLite code must not be busy")
	}
}
