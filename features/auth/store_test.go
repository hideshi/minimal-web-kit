package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newAdminFixture(t *testing.T) (*adminStore, *sql.DB, *fixedClock) {
	t.Helper()
	_, db := newTestDB(t)
	clock := &fixedClock{t: time.Unix(1_700_000_000, 0)}
	return newAdminStore(db, clock.now), db, clock
}

func testCredential(id string) newCredential {
	return newCredential{ID: []byte(id), PublicKey: []byte("spki-" + id), Alg: algES256, SignCount: 0}
}

func TestAdminSession_IdleAndAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)

	token, err := s.createSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM auth_sessions").Scan(&stored); err != nil || stored == token || stored != hashToken(token) {
		t.Fatal("only the SHA-256 hash of the token must be stored")
	}
	if err := s.validateSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := s.validateSession(ctx, "unknown"); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("unknown token must be rejected")
	}

	// 操作を続ければ、無操作の期限は延びる
	for i := 0; i < 5; i++ {
		clock.advance(adminSessionIdle - time.Minute)
		if err := s.validateSession(ctx, token); err != nil {
			t.Fatalf("active session expired after %d renewals: %v", i, err)
		}
	}
	// 操作を続けても、発行から12時間で失効する
	clock.advance(adminSessionMax)
	if err := s.validateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("absolute expiry must apply")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 0)

	// 無操作で2時間を過ぎると失効する
	idle, err := s.createSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(adminSessionIdle)
	if err := s.validateSession(ctx, idle); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("idle expiry must apply")
	}
}

func TestAdminSession_DeleteAndDeleteOthers(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	a, _ := s.createSession(ctx)
	b, _ := s.createSession(ctx)
	c, _ := s.createSession(ctx)
	if err := s.deleteOtherSessions(ctx, b); err != nil {
		t.Fatal(err)
	}
	if s.validateSession(ctx, a) == nil || s.validateSession(ctx, c) == nil || s.validateSession(ctx, b) != nil {
		t.Fatal("only the kept session must remain")
	}
	if err := s.deleteSession(ctx, b); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 0)
}

func TestSetup_IssuesTokenOnlyWithoutPasskeys(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)

	first, err := Setup(ctx, db, clock.now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Setup(ctx, db, clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if s.checkSetupToken(ctx, first) == nil {
		t.Fatal("re-running setup must invalidate the previous unused token")
	}
	if err := s.checkSetupToken(ctx, second); err != nil {
		t.Fatal(err)
	}
	// 確認だけでは消費しない
	if err := s.checkSetupToken(ctx, second); err != nil {
		t.Fatal("checking must not consume the token")
	}
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM auth_setup_tokens").Scan(&stored); err != nil || stored == second {
		t.Fatal("only the hash of the setup token must be stored")
	}

	if err := s.registerWithSetupToken(ctx, second, testCredential("key-1"), "Windows Hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := Setup(ctx, db, clock.now()); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("setup must refuse once a passkey exists, got %v", err)
	}
}

func TestSetupToken_SingleUseExpiryAndAtomicity(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	token, err := Setup(ctx, db, clock.now())
	if err != nil {
		t.Fatal(err)
	}

	// 不正な端末名ではトークンを消費しない
	if err := s.registerWithSetupToken(ctx, token, testCredential("key-1"), "  "); !errors.Is(err, ErrInvalidInput) {
		t.Fatal(err)
	}
	if err := s.checkSetupToken(ctx, token); err != nil {
		t.Fatal("invalid label must not consume the token")
	}

	// 並行して使っても、成功は1件だけ
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.registerWithSetupToken(ctx, token, testCredential("key-"+strings.Repeat("x", i+1)), "key") == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("expected exactly one registration, got %d", successes)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 1)

	// 期限切れのトークンは使えない
	expired, err := Recover(ctx, db, clock.now())
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(setupTokenTTL)
	if err := s.checkSetupToken(ctx, expired); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("expired token must be rejected")
	}
	if err := s.registerWithSetupToken(ctx, expired, testCredential("key-late"), "key"); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("expired token must not register")
	}
}

func TestRegisterWithSetupToken_DuplicateKeepsTokenUnused(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	if err := s.addCredential(ctx, testCredential("dup"), "existing"); err != nil {
		t.Fatal(err)
	}
	// Setupは登録済みでは使えないため、復旧用のトークンの代わりに直接発行する
	tx, _ := db.Begin()
	token, err := issueSetupToken(ctx, tx, clock.now())
	if err != nil || tx.Commit() != nil {
		t.Fatal(err)
	}
	if err := s.registerWithSetupToken(ctx, token, testCredential("dup"), "again"); !errors.Is(err, ErrCredentialExists) {
		t.Fatalf("duplicate credential id must be rejected by the primary key, got %v", err)
	}
	if err := s.checkSetupToken(ctx, token); err != nil {
		t.Fatal("a failed registration must leave the token unused")
	}
}

func TestCredentials_RecordLoginIsMonotonic(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newAdminFixture(t)
	c := testCredential("key")
	c.SignCount = 5
	if err := s.addCredential(ctx, c, "key"); err != nil {
		t.Fatal(err)
	}
	if err := s.recordLogin(ctx, c.ID, 7); err != nil {
		t.Fatal(err)
	}
	// 並行ログインで古い値が後から書かれても、カウンタは戻らない
	if err := s.recordLogin(ctx, c.ID, 6); !errors.Is(err, ErrSignCountConflict) {
		t.Fatalf("got %v", err)
	}
	got, err := s.getCredential(ctx, c.ID)
	if err != nil || got.SignCount != 7 {
		t.Fatalf("sign count = %d, err=%v", got.SignCount, err)
	}

	zero := testCredential("zero")
	if err := s.addCredential(ctx, zero, "zero"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := s.recordLogin(ctx, zero.ID, 0); err != nil {
			t.Fatal("authenticators without counters must keep working")
		}
	}
}

func TestCredentials_DisableRevokesAllSessions(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	c := testCredential("key")
	if err := s.addCredential(ctx, c, "key"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.createSession(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.disableCredential(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 0)
	if _, err := s.getCredential(ctx, c.ID); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatal("disabled credential must be unavailable")
	}
	if err := s.recordLogin(ctx, c.ID, 1); !errors.Is(err, ErrSignCountConflict) {
		t.Fatal("disabled credential must not record logins")
	}
	if _, err := s.getCredential(ctx, []byte("missing")); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatal("unknown credential must be unavailable")
	}
}

// 復旧では、管理者認証のデータだけを消し、他の機能のデータ（例: CMSの記事）は残すこと。
func TestRecover_ClearsAuthStateButKeepsOtherData(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	if _, err := db.Exec("CREATE TABLE other_data (v TEXT); INSERT INTO other_data VALUES ('keep')"); err != nil {
		t.Fatal(err)
	}
	if err := s.addCredential(ctx, testCredential("lost"), "lost key"); err != nil {
		t.Fatal(err)
	}
	session, _ := s.createSession(ctx)
	oldUserID, _ := s.webAuthnUserID(ctx)
	tx, _ := db.Begin()
	staleToken, _ := issueSetupToken(ctx, tx, clock.now())
	_ = tx.Commit()

	token, err := Recover(ctx, db, clock.now())
	if err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 0)
	if s.validateSession(ctx, session) == nil {
		t.Fatal("recovery must revoke admin sessions")
	}
	if s.checkSetupToken(ctx, staleToken) == nil {
		t.Fatal("recovery must revoke previous setup tokens")
	}
	if err := s.checkSetupToken(ctx, token); err != nil {
		t.Fatal(err)
	}
	newUserID, _ := s.webAuthnUserID(ctx)
	if len(newUserID) != 16 || string(newUserID) == string(oldUserID) {
		t.Fatal("recovery must regenerate the WebAuthn user id")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM other_data", 1)
}

func TestSetupURL_UsesFragment(t *testing.T) {
	u := SetupURL("http://localhost:8080", "abc")
	if u != "http://localhost:8080/admin/setup#token=abc" || strings.Contains(u, "?") {
		t.Fatalf("setup URL must carry the token in the fragment: %s", u)
	}
}

func TestLabel_Validation(t *testing.T) {
	for _, bad := range []string{"", "   ", "a\nb", strings.Repeat("あ", maxLabelRunes+1), "\x00"} {
		if _, err := normalizeLabel(bad); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("label %q must be rejected", bad)
		}
	}
	if got, err := normalizeLabel("  Windows Hello  "); err != nil || got != "Windows Hello" {
		t.Fatal(got, err)
	}
}

func TestMigrate_IdempotentAndKeepsUserID(t *testing.T) {
	_, db := newTestDB(t)
	var userID []byte
	if err := db.QueryRow("SELECT webauthn_user_id FROM auth_settings WHERE id = 1").Scan(&userID); err != nil || len(userID) != 16 {
		t.Fatalf("webauthn user id: %v len=%d", err, len(userID))
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	var again []byte
	if err := db.QueryRow("SELECT webauthn_user_id FROM auth_settings WHERE id = 1").Scan(&again); err != nil || string(again) != string(userID) {
		t.Fatal("re-running the migration must keep the WebAuthn user id")
	}
}
