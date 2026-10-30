// This experiment checks a proposed revocation contract, not features/auth behavior.
package experiment

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"minimal-web-kit/internal/store"
)

// TestGenerationExperiment uses the kit's actual driver and DSN, a WAL file,
// and independent connections to check exchange/disable serialization.
func TestGenerationExperiment(t *testing.T) {
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "probe.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db := s.DB()
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE probe_accounts(id INTEGER PRIMARY KEY, enabled INTEGER, generation INTEGER);
 CREATE TABLE probe_codes(code TEXT PRIMARY KEY, account INTEGER, generation INTEGER, used INTEGER);
 INSERT INTO probe_accounts VALUES(1,1,1),(2,1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	exchange := func(code string) (int64, error) {
		c, e := db.Conn(ctx)
		if e != nil {
			return 0, e
		}
		defer c.Close()
		if _, e = c.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
			return 0, e
		}
		defer c.ExecContext(ctx, "ROLLBACK")
		var gen int64
		e = c.QueryRowContext(ctx, `UPDATE probe_codes SET used=1 WHERE code=? AND used=0
   AND EXISTS(SELECT 1 FROM probe_accounts a WHERE a.id=account AND a.enabled=1 AND a.generation=probe_codes.generation)
   RETURNING generation`, code).Scan(&gen)
		if errors.Is(e, sql.ErrNoRows) {
			return 0, nil
		}
		if e != nil {
			return 0, e
		}
		_, e = c.ExecContext(ctx, "COMMIT")
		return gen, e
	}
	stopResume := func() error {
		c, e := db.Conn(ctx)
		if e != nil {
			return e
		}
		defer c.Close()
		if _, e = c.ExecContext(ctx, "BEGIN IMMEDIATE"); e != nil {
			return e
		}
		defer c.ExecContext(ctx, "ROLLBACK")
		if _, e = c.ExecContext(ctx, "UPDATE probe_accounts SET enabled=0,generation=generation+1 WHERE id=1"); e != nil {
			return e
		}
		if _, e = c.ExecContext(ctx, "DELETE FROM probe_codes WHERE account=1"); e != nil {
			return e
		}
		if _, e = c.ExecContext(ctx, "COMMIT"); e != nil {
			return e
		}
		_, e = c.ExecContext(ctx, "UPDATE probe_accounts SET enabled=1 WHERE id=1")
		return e
	}
	active := func(id, gen int64) bool {
		var n int
		if e := db.QueryRow("SELECT count(*) FROM probe_accounts WHERE id=? AND enabled=1 AND generation=?", id, gen).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n == 1
	}
	issue := func(code string) {
		if _, e := db.Exec("INSERT INTO probe_codes SELECT ?,id,generation,0 FROM probe_accounts WHERE id=1 AND enabled=1", code); e != nil {
			t.Fatal(e)
		}
	}
	issue("first")
	old, e := exchange("first")
	if e != nil || old != 1 || !active(1, old) {
		t.Fatalf("initial exchange: %v %v", old, e)
	}
	issue("pending")
	if e = stopResume(); e != nil {
		t.Fatal(e)
	}
	if active(1, old) || !active(2, 1) {
		t.Fatal("revocation isolation")
	}
	if g, e := exchange("pending"); g != 0 || e != nil {
		t.Fatalf("pending code survived: %v %v", g, e)
	}
	for i := 0; i < 40; i++ {
		issue("race")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var issued int64
		var exchangeErr, stopErr error
		go func() { defer wg.Done(); <-start; issued, exchangeErr = exchange("race") }()
		go func() { defer wg.Done(); <-start; stopErr = stopResume() }()
		close(start)
		wg.Wait()
		if exchangeErr != nil || stopErr != nil {
			t.Fatalf("iteration %d: exchange=%v stop=%v", i, exchangeErr, stopErr)
		}
		if issued != 0 && active(1, issued) {
			t.Fatalf("iteration %d: old generation survived", i)
		}
		if g, e := exchange("race"); g != 0 || e != nil {
			t.Fatalf("iteration %d: code survived: %v %v", i, g, e)
		}
	}
	issue("fresh")
	fresh, e := exchange("fresh")
	if e != nil || fresh == 0 || !active(1, fresh) {
		t.Fatalf("fresh login: %v %v", fresh, e)
	}
	if g, e := exchange("fresh"); g != 0 || e != nil {
		t.Fatalf("code reuse accepted: %v %v", g, e)
	}
}
