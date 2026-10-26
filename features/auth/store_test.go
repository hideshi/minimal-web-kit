package auth

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
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

// newAccount はテスト用のアカウントを作ります。
func newAccount(t *testing.T, s *adminStore, name, role string) int64 {
	t.Helper()
	id, err := insertAccount(context.Background(), s.db, name, role, s.now())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// newSession はテスト用に、アカウントの管理者セッションを作ります。
func newSession(t *testing.T, s *adminStore, accountID int64) string {
	t.Helper()
	token, err := createSession(context.Background(), s.db, accountID, s.now())
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// 不変条件: AUTH-INV-11, AUTH-INV-12
func TestAdminSession_IdleAndAbsoluteExpiry(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	id := newAccount(t, s, "管理者", roleAdmin)

	token := newSession(t, s, id)
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM auth_sessions").Scan(&stored); err != nil || stored == token || stored != hashToken(token) {
		t.Fatal("only the SHA-256 hash of the token must be stored")
	}
	got, err := s.validateSession(ctx, token)
	if err != nil || got.AccountID != id || got.Role != roleAdmin {
		t.Fatalf("session account = %+v, err=%v", got, err)
	}
	if _, err := s.validateSession(ctx, "unknown"); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("unknown token must be rejected")
	}

	// 操作を続ければ、無操作の期限は延びる
	for i := 0; i < 5; i++ {
		clock.advance(adminSessionIdle - time.Minute)
		if _, err := s.validateSession(ctx, token); err != nil {
			t.Fatalf("active session expired after %d renewals: %v", i, err)
		}
	}
	// 操作を続けても、発行から12時間で失効する
	clock.advance(adminSessionMax)
	if _, err := s.validateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("absolute expiry must apply")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 0)

	// 無操作で2時間を過ぎると失効する
	idle := newSession(t, s, id)
	clock.advance(adminSessionIdle)
	if _, err := s.validateSession(ctx, idle); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("idle expiry must apply")
	}
}

// ロールは要求ごとにアカウントから読み、停止中のアカウントのセッションは使えないこと。
// 不変条件: AUTH-INV-5
func TestAdminSession_FollowsAccountState(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	id := newAccount(t, s, "メンバーA", roleMember)
	token := newSession(t, s, id)
	mustExec(t, db, "UPDATE auth_accounts SET role = 'admin' WHERE id = ?", id)
	if got, err := s.validateSession(ctx, token); err != nil || got.Role != roleAdmin {
		t.Fatalf("role must be read per request: %+v %v", got, err)
	}
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", id)
	if _, err := s.validateSession(ctx, token); !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("a disabled account's session must be rejected")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 0)
}

// 不変条件: AUTH-INV-7, AUTH-INV-11, AUTH-INV-15
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
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts WHERE role = 'admin' AND name = '管理者'", 1)
	if _, err := s.checkSetupToken(ctx, first); err == nil {
		t.Fatal("re-running setup must invalidate the previous unused token")
	}
	target, err := s.checkSetupToken(ctx, second)
	if err != nil || target.Name != firstAdminName || len(target.WebAuthnUserID) != 16 {
		t.Fatalf("target = %+v, err=%v", target, err)
	}
	// 確認だけでは消費しない
	if _, err := s.checkSetupToken(ctx, second); err != nil {
		t.Fatal("checking must not consume the token")
	}
	var stored string
	if err := db.QueryRow("SELECT token_hash FROM auth_setup_tokens").Scan(&stored); err != nil || stored == second {
		t.Fatal("only the hash of the setup token must be stored")
	}

	if err := s.registerWithSetupToken(ctx, second, testCredential("key-1"), "Windows Hello"); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials WHERE account_id = "+itoa(target.AccountID), 1)
	if _, err := Setup(ctx, db, clock.now()); !errors.Is(err, ErrAlreadySetUp) {
		t.Fatalf("setup must refuse once a passkey exists, got %v", err)
	}
}

// 不変条件: AUTH-INV-7
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
	if _, err := s.checkSetupToken(ctx, token); err != nil {
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
	expired, err := Recover(ctx, db, clock.now(), RecoverOptions{AccountID: 1})
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(setupTokenTTL)
	if _, err := s.checkSetupToken(ctx, expired); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("expired token must be rejected")
	}
	if err := s.registerWithSetupToken(ctx, expired, testCredential("key-late"), "key"); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("expired token must not register")
	}
}

// 停止中のアカウントの登録トークンは使えないこと。トークンの発行し直しは、他のアカウントのトークンに触れないこと。
// 不変条件: AUTH-INV-3, AUTH-INV-5, AUTH-INV-7
func TestSetupToken_ScopedToAccount(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	a := newAccount(t, s, "A", roleMember)
	b := newAccount(t, s, "B", roleMember)
	ta, err := issueSetupToken(ctx, db, a, inviteTokenTTL, clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueSetupToken(ctx, db, b, inviteTokenTTL, clock.now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.checkSetupToken(ctx, ta); err != nil {
		t.Fatal("issuing a token for B must keep A's token")
	}
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", a)
	if _, err := s.checkSetupToken(ctx, ta); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("a disabled account's token must be rejected")
	}
	if err := s.registerWithSetupToken(ctx, ta, testCredential("k"), "key"); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("a disabled account's token must not register")
	}
	// 24時間の招待トークン
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", a)
	clock.advance(inviteTokenTTL - time.Minute)
	if _, err := s.checkSetupToken(ctx, ta); err != nil {
		t.Fatal("an invite token must last 24 hours")
	}
	clock.advance(time.Minute)
	if _, err := s.checkSetupToken(ctx, ta); !errors.Is(err, ErrSetupTokenInvalid) {
		t.Fatal("an invite token must expire after 24 hours")
	}
}

// 不変条件: AUTH-INV-7
func TestRegisterWithSetupToken_DuplicateKeepsTokenUnused(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	id := newAccount(t, s, "管理者", roleAdmin)
	if err := s.addCredential(ctx, id, testCredential("dup"), "existing"); err != nil {
		t.Fatal(err)
	}
	token, err := issueSetupToken(ctx, db, id, setupTokenTTL, clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.registerWithSetupToken(ctx, token, testCredential("dup"), "again"); !errors.Is(err, ErrCredentialExists) {
		t.Fatalf("duplicate credential id must be rejected by the primary key, got %v", err)
	}
	if _, err := s.checkSetupToken(ctx, token); err != nil {
		t.Fatal("a failed registration must leave the token unused")
	}
}

// 不変条件: AUTH-INV-3, AUTH-INV-5, AUTH-INV-8
func TestLogin_CounterMonotonicAndSession(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	id := newAccount(t, s, "管理者", roleAdmin)
	c := testCredential("key")
	c.SignCount = 5
	if err := s.addCredential(ctx, id, c, "key"); err != nil {
		t.Fatal(err)
	}
	token, err := s.login(ctx, c.ID, id, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.validateSession(ctx, token); err != nil || got.AccountID != id {
		t.Fatal("login must create a session for the account")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts WHERE last_login_at IS NOT NULL", 1)
	// 並行ログインで古い値が後から書かれても、カウンタは戻らない
	if _, err := s.login(ctx, c.ID, id, 6); !errors.Is(err, ErrSignCountConflict) {
		t.Fatalf("got %v", err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 1)
	got, err := s.getCredential(ctx, c.ID)
	if err != nil || got.SignCount != 7 || got.AccountID != id {
		t.Fatalf("credential = %+v, err=%v", got, err)
	}
	// 別のアカウントとしてはログインできない
	other := newAccount(t, s, "B", roleMember)
	if _, err := s.login(ctx, c.ID, other, 8); !errors.Is(err, ErrSignCountConflict) {
		t.Fatal("a credential must not log in as another account")
	}

	zero := testCredential("zero")
	if err := s.addCredential(ctx, id, zero, "zero"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.login(ctx, zero.ID, id, 0); err != nil {
			t.Fatal("authenticators without counters must keep working")
		}
	}
	// 停止中のアカウントにはセッションを作らない
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", id)
	if got, _ := s.getCredential(ctx, zero.ID); !got.AccountDisabled {
		t.Fatal("getCredential must report the disabled account")
	}
	if _, err := s.login(ctx, zero.ID, id, 0); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("a disabled account must not log in, got %v", err)
	}
}

// 不変条件: AUTH-INV-8
func TestCredentials_DisableRevokesAccountSessions(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	id := newAccount(t, s, "管理者", roleAdmin)
	other := newAccount(t, s, "B", roleMember)
	c := testCredential("key")
	if err := s.addCredential(ctx, id, c, "key"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		newSession(t, s, id)
	}
	kept := newSession(t, s, other)
	if err := s.disableCredential(ctx, c.ID, id); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 1)
	if _, err := s.validateSession(ctx, kept); err != nil {
		t.Fatal("other accounts' sessions must be kept")
	}
	if _, err := s.getCredential(ctx, c.ID); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatal("disabled credential must be unavailable")
	}
	if _, err := s.login(ctx, c.ID, id, 1); !errors.Is(err, ErrSignCountConflict) {
		t.Fatal("disabled credential must not log in")
	}
	if _, err := s.getCredential(ctx, []byte("missing")); !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatal("unknown credential must be unavailable")
	}
}

// 全体の復旧では、管理者認証の鍵とセッションだけを消し、アカウントと他の機能のデータ（例: CMSの記事）は残すこと。
// 不変条件: AUTH-INV-16
func TestRecover_ClearsAuthStateButKeepsOtherData(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	mustExec(t, db, "CREATE TABLE other_data (v TEXT); INSERT INTO other_data VALUES ('keep')")
	admin := newAccount(t, s, "管理者", roleAdmin)
	member := newAccount(t, s, "メンバーA", roleMember)
	if err := s.addCredential(ctx, admin, testCredential("lost"), "lost key"); err != nil {
		t.Fatal(err)
	}
	if err := s.addCredential(ctx, member, testCredential("m"), "member key"); err != nil {
		t.Fatal(err)
	}
	session := newSession(t, s, member)
	staleToken, _ := issueSetupToken(ctx, db, member, inviteTokenTTL, clock.now())
	var oldUserID []byte
	_ = db.QueryRow("SELECT webauthn_user_id FROM auth_accounts WHERE id = ?", admin).Scan(&oldUserID)

	// 管理者がいれば、登録し直す管理者の指定が必要。メンバーは指定できない
	if _, err := Recover(ctx, db, clock.now(), RecoverOptions{}); !errors.Is(err, ErrChooseAccount) {
		t.Fatalf("recover must ask which admin, got %v", err)
	}
	if _, err := Recover(ctx, db, clock.now(), RecoverOptions{AccountID: member}); !errors.Is(err, ErrChooseAccount) {
		t.Fatal("a full recover must re-register an admin")
	}
	if _, err := Recover(ctx, db, clock.now(), RecoverOptions{AccountID: 999}); !errors.Is(err, ErrNotFound) {
		t.Fatal("unknown account must be rejected")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 2)

	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", admin)
	token, err := Recover(ctx, db, clock.now(), RecoverOptions{AccountID: admin})
	if err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials", 0)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts", 2)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts WHERE disabled_at IS NOT NULL", 0)
	if _, err := s.validateSession(ctx, session); err == nil {
		t.Fatal("recovery must revoke admin sessions")
	}
	if _, err := s.checkSetupToken(ctx, staleToken); err == nil {
		t.Fatal("recovery must revoke previous setup tokens")
	}
	target, err := s.checkSetupToken(ctx, token)
	if err != nil || target.AccountID != admin {
		t.Fatal("the token must belong to the chosen admin")
	}
	if len(target.WebAuthnUserID) != 16 || string(target.WebAuthnUserID) == string(oldUserID) {
		t.Fatal("recovery must regenerate the WebAuthn user id")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM other_data", 1)
}

// -only は、指定したアカウントだけを初期化し、他のアカウントの鍵とセッションは残すこと。
// 不変条件: AUTH-INV-3, AUTH-INV-16
func TestRecover_OnlyKeepsOtherAccounts(t *testing.T) {
	ctx := context.Background()
	s, db, clock := newAdminFixture(t)
	a := newAccount(t, s, "管理者A", roleAdmin)
	b := newAccount(t, s, "管理者B", roleAdmin)
	_ = s.addCredential(ctx, a, testCredential("a"), "a")
	_ = s.addCredential(ctx, b, testCredential("b"), "b")
	keep := newSession(t, s, b)
	newSession(t, s, a)
	if _, err := Recover(ctx, db, clock.now(), RecoverOptions{Only: true}); !errors.Is(err, ErrChooseAccount) {
		t.Fatal("-only needs an account")
	}
	token, err := Recover(ctx, db, clock.now(), RecoverOptions{AccountID: a, Only: true})
	if err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials WHERE account_id = "+itoa(a), 0)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_credentials WHERE account_id = "+itoa(b), 1)
	if _, err := s.validateSession(ctx, keep); err != nil {
		t.Fatal("-only must keep other accounts' sessions")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_sessions", 1)
	if target, err := s.checkSetupToken(ctx, token); err != nil || target.AccountID != a {
		t.Fatal("the token must belong to the account")
	}
}

// 不変条件: AUTH-INV-11
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

// 不変条件: AUTH-INV-16
func TestMigrate_IdempotentAndDetectsOldSchema(t *testing.T) {
	ctx := context.Background()
	s, db, _ := newAdminFixture(t)
	newAccount(t, s, "管理者", roleAdmin)
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts", 1)

	// アカウント導入前の形式（auth_settings と、account_id の無い表）は起動を止め、-reset-schema で作り直す
	mustExec(t, db, "CREATE TABLE other_data (v TEXT); INSERT INTO other_data VALUES ('keep')")
	mustExec(t, db, `DROP TABLE auth_setup_tokens; DROP TABLE auth_sessions; DROP TABLE auth_credentials; DROP TABLE auth_accounts;
		CREATE TABLE auth_settings (id INTEGER PRIMARY KEY, webauthn_user_id BLOB);
		CREATE TABLE auth_credentials (id BLOB PRIMARY KEY, public_key BLOB)`)
	if err := Migrate(db); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("old schema must be detected, got %v", err)
	}
	mustExec(t, db, "DROP TABLE auth_settings")
	if err := Migrate(db); !errors.Is(err, ErrOldSchema) {
		t.Fatal("credentials without account_id must be detected")
	}
	if err := ResetSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_accounts", 0)
	assertRows(t, db, "SELECT COUNT(*) FROM other_data", 1)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
