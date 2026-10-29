package store

import (
	"errors"
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
