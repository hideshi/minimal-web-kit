package page

import (
	"net/http/httptest"
	"testing"
)

func TestParseAndTrim(t *testing.T) {
	for _, tc := range []struct {
		query         string
		ok            bool
		number, prev  int
		limit, offset int
	}{
		{"", true, 1, 0, 11, 0},
		{"?page=1", true, 1, 0, 11, 0},
		{"?page=3", true, 3, 2, 11, 20},
		{"?page=0", false, 0, 0, 0, 0},
		{"?page=-1", false, 0, 0, 0, 0},
		{"?page=abc", false, 0, 0, 0, 0},
		{"?page=101", false, 0, 0, 0, 0},
		{"?page=1e3", false, 0, 0, 0, 0},
	} {
		p, ok := Parse(httptest.NewRequest("GET", "/news"+tc.query, nil), 10, 100)
		if ok != tc.ok {
			t.Errorf("%q: ok=%v", tc.query, ok)
			continue
		}
		if ok && (p.Number != tc.number || p.Prev != tc.prev || p.Limit() != tc.limit || p.Offset() != tc.offset) {
			t.Errorf("%q: got %+v limit=%d offset=%d", tc.query, p, p.Limit(), p.Offset())
		}
	}

	p, _ := Parse(httptest.NewRequest("GET", "/?page=2", nil), 3, 10)
	if got := Trim(&p, []int{1, 2, 3, 4}); len(got) != 3 || p.Next != 3 {
		t.Fatalf("more items: got %v next=%d", got, p.Next)
	}
	if got := Trim(&p, []int{1, 2, 3}); len(got) != 3 || p.Next != 0 {
		t.Fatalf("last page: got %v next=%d", got, p.Next)
	}
}
