package auth

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// newFederationFixture は、連携先1件と、利用許可のあるアカウント1件を作ります。
func newFederationFixture(t *testing.T) (*FederationStore, *sql.DB, FederationClient, int64, string) {
	t.Helper()
	_, db := newTestDB(t)
	ctx := context.Background()
	s := NewFederationStore(db)
	client, _, err := s.CreateClient(ctx, "アプリB", "https://b.example/admin/sso/callback")
	if err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("INSERT INTO auth_accounts (name, role, webauthn_user_id, created_at) VALUES ('見本', 'member', x'01', 1)")
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	subject, err := s.Grant(ctx, client.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, client, id, subject
}

// Atomic はコミットの失敗で成功を返さず、接続を再利用できること。意図した失効だけは、エラーでもコミットすること。
func TestFederationAtomic_CommitFailureAndReplay(t *testing.T) {
	s, db, client, _, _ := newFederationFixture(t)
	ctx := context.Background()

	// 遅延の外部キー違反で、COMMITを失敗させる
	err := s.Atomic(ctx, func(ctx context.Context) error {
		x := s.executor(ctx)
		if _, err := x.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON"); err != nil {
			return err
		}
		_, err := x.ExecContext(ctx, "INSERT INTO auth_oidc_client_secrets (client_id, secret_hash, expires_at) VALUES ('missing', 'h', 1)")
		return err
	})
	if err == nil {
		t.Fatal("commit failure was reported as success")
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_client_secrets WHERE client_id = 'missing'", 0)
	if err := s.Atomic(ctx, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("connection not reusable after failure: %v", err)
	}

	failed := errors.New("failed")
	err = s.Atomic(ctx, func(ctx context.Context) error {
		if _, err := s.executor(ctx).ExecContext(ctx, "UPDATE auth_oidc_clients SET label = 'changed' WHERE client_id = ?", client.ID); err != nil {
			return err
		}
		return failed
	})
	if !errors.Is(err, failed) {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_clients WHERE label = 'changed'", 0)

	err = s.Atomic(ctx, func(ctx context.Context) error {
		if _, err := s.executor(ctx).ExecContext(ctx, "UPDATE auth_oidc_clients SET label = 'revoked' WHERE client_id = ?", client.ID); err != nil {
			return err
		}
		return ErrFederationReplay
	})
	if !errors.Is(err, ErrFederationReplay) {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_clients WHERE label = 'revoked'", 1)
}

// 停止・鍵のリセット・削除・複製の疑いは、経路に関わらず世代を進めて、認証済みの要求を消すこと。
// 再開では世代を戻さないこと。
//
// 不変条件: AUTH-INV-17
func TestFederationEpochs_AdvanceOnEveryRevocation(t *testing.T) {
	s, db, client, id, subject := newFederationFixture(t)
	ctx := context.Background()
	account := func() string {
		var e string
		if err := db.QueryRow("SELECT account_epoch FROM auth_federated_subjects WHERE sub = ?", subject).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	bound := func() {
		t.Helper()
		mustExec(t, db, `INSERT INTO auth_oidc_requests (request_id, flow_hash, client_id, redirect_uri, state, nonce, challenge, sub, expires_at)
			VALUES ('r', 'f', ?, 'https://b.example/admin/sso/callback', 's', 'n', 'c', ?, ?)`, client.ID, subject, time.Now().Add(time.Hour).Unix())
	}
	bound()
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = 1 WHERE id = ?", id)
	if account() != "2" {
		t.Fatalf("stop: %s", account())
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_requests", 0)
	mustExec(t, db, "UPDATE auth_accounts SET disabled_at = NULL WHERE id = ?", id)
	if account() != "2" {
		t.Fatalf("restart must not change the epoch: %s", account())
	}
	if _, err := s.Epochs(ctx, client.ID, subject); err != nil {
		t.Fatal(err)
	}

	bound()
	if err := resetAccountKeys(ctx, db, id); err != nil {
		t.Fatal(err)
	}
	if account() != "3" {
		t.Fatalf("reset: %s", account())
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_requests", 0)

	mustExec(t, db, "INSERT INTO auth_credentials (id, account_id, public_key, alg, label, created_at) VALUES (x'02', ?, x'00', -7, '端末', 1)", id)
	bound()
	mustExec(t, db, "UPDATE auth_credentials SET disabled_at = 1 WHERE account_id = ?", id)
	if account() != "4" {
		t.Fatalf("clone suspicion: %s", account())
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_requests", 0)

	mustExec(t, db, "DELETE FROM auth_accounts WHERE id = ?", id)
	if account() != "5" {
		t.Fatalf("delete: %s", account())
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_federated_subjects WHERE deleted_at IS NOT NULL AND account_id IS NULL", 1)
	if _, err := s.Epochs(ctx, client.ID, subject); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted account is valid: %v", err)
	}
}

// 墓標と消費済みの記録は保持期間の間は残り、過ぎたらGCで消えること。利用許可も一緒に消えること。
//
// 不変条件: AUTH-INV-16, AUTH-INV-17
func TestFederationPurge_KeepsTombstonesUntilTheyExpire(t *testing.T) {
	s, db, client, id, subject := newFederationFixture(t)
	ctx := context.Background()
	mustExec(t, db, "DELETE FROM auth_accounts WHERE id = ?", id)
	mustExec(t, db, `INSERT INTO auth_oidc_requests (request_id, flow_hash, client_id, redirect_uri, state, nonce, challenge, consumed_at, expires_at)
		VALUES ('kept', 'f', ?, 'u', 's', 'n', 'c', 1, ?), ('gone', 'g', ?, 'u', 's', 'n', 'c', 1, 1)`, client.ID, time.Now().Add(time.Hour).Unix(), client.ID)
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_federated_subjects", 1)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_requests", 1)

	s.now = func() time.Time { return time.Now().Add(federationTombstoneTTL + time.Minute) }
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_federated_subjects", 0)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_grants WHERE sub = '"+subject+"'", 0)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_requests", 0)
}

// 秘密値の更新は24時間だけ旧値を併用し、漏洩時は直ちに無効にして世代を進めること。
//
// 不変条件: AUTH-INV-17
func TestFederationSecrets_Rotation(t *testing.T) {
	_, db := newTestDB(t)
	ctx := context.Background()
	s := NewFederationStore(db)
	client, first, err := s.CreateClient(ctx, "アプリB", "https://b.example/cb")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RotateSecret(ctx, client.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Authenticate(ctx, client.ID, first); err != nil {
		t.Fatalf("old secret during grace: %v", err)
	}
	if err := s.Authenticate(ctx, client.ID, second); err != nil {
		t.Fatal(err)
	}
	third, err := s.RotateSecret(ctx, client.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_client_secrets", 2)
	if err := s.Authenticate(ctx, client.ID, first); !errors.Is(err, ErrFederationInvalid) {
		t.Fatalf("a third secret generation stayed valid: %v", err)
	}
	s.now = func() time.Time { return time.Now().Add(federationSecretGrace + time.Minute) }
	if err := s.Authenticate(ctx, client.ID, second); !errors.Is(err, ErrFederationInvalid) {
		t.Fatalf("old secret after grace: %v", err)
	}
	s.now = time.Now
	fourth, err := s.RotateSecret(ctx, client.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Authenticate(ctx, client.ID, third); !errors.Is(err, ErrFederationInvalid) {
		t.Fatalf("compromised secret stayed valid: %v", err)
	}
	if err := s.Authenticate(ctx, client.ID, fourth); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_clients WHERE client_epoch = 2", 1)
	if err := s.Authenticate(ctx, client.ID, strings.Repeat("x", 300)); !errors.Is(err, ErrFederationInvalid) {
		t.Fatal(err)
	}
}

// 利用許可は、付与し直しても有効な間は世代を変えず（既存のログインを巻き込まない）、取り消しの後の再付与では世代を進めること。
//
// 不変条件: AUTH-INV-17
func TestFederationGrant_Epochs(t *testing.T) {
	s, db, client, id, subject := newFederationFixture(t)
	ctx := context.Background()
	again, err := s.Grant(ctx, client.ID, id)
	if err != nil || again != subject {
		t.Fatalf("%v %v", again == subject, err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_grants WHERE grant_epoch = 1", 1)
	if err := s.Revoke(ctx, client.ID, id); err != nil {
		t.Fatal(err)
	}
	if err := s.Revoke(ctx, client.ID, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second revoke: %v", err)
	}
	if _, err := s.Epochs(ctx, client.ID, subject); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked grant is valid: %v", err)
	}
	if _, err := s.Grant(ctx, client.ID, id); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_grants WHERE grant_epoch = 3 AND allowed = 1", 1)
}

// 未完了の認可要求は上限で止まり、期限切れは数えないこと。ブラウザごとに古い要求を消せること。
func TestFederationRequests_Capacity(t *testing.T) {
	s, db, client, _, _ := newFederationFixture(t)
	ctx := context.Background()
	for i := range federationMaxRequests {
		mustExec(t, db, `INSERT INTO auth_oidc_requests (request_id, flow_hash, client_id, redirect_uri, state, nonce, challenge, expires_at)
			VALUES (?, ?, ?, 'u', 's', 'n', 'c', ?)`, i, i, client.ID, time.Now().Add(time.Minute).Unix())
	}
	request := FederationRequest{ID: "new", Client: client.ID, Redirect: "u", State: "s", Nonce: "n", Challenge: "c"}
	if err := s.NewRequest(ctx, "flow", request); !errors.Is(err, ErrFederationBusy) {
		t.Fatalf("full: %v", err)
	}
	mustExec(t, db, "UPDATE auth_oidc_requests SET expires_at = 1 WHERE request_id = '0'")
	if err := s.NewRequest(ctx, "flow", request); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flow(ctx, "flow"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveFlow(ctx, "flow"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Flow(ctx, "flow"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
}

// 連携の保存処理は、不正な戻り先を登録しないこと。
func TestFederationClients_RejectUnsafeRedirects(t *testing.T) {
	_, db := newTestDB(t)
	s := NewFederationStore(db)
	for _, redirect := range []string{"http://b.example/cb", "https://b.example/cb?x=1", "https://b.example/cb#f", "https://u@b.example/cb", "not a url", ""} {
		if _, _, err := s.CreateClient(context.Background(), "x", redirect); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%q: %v", redirect, err)
		}
	}
}

// 全体の復旧とスキーマの作り直しは、連携の状態も対象にすること。
//
// 不変条件: AUTH-INV-16, AUTH-INV-17
func TestFederationRecovery_CoversFederationTables(t *testing.T) {
	s, db, client, id, subject := newFederationFixture(t)
	ctx := context.Background()
	mustExec(t, db, "UPDATE auth_accounts SET role = 'admin' WHERE id = ?", id)
	second, err := db.Exec("INSERT INTO auth_accounts (name, role, webauthn_user_id, created_at) VALUES ('二人目', 'admin', x'03', 1)")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := second.LastInsertId()
	if _, err := Recover(ctx, db, time.Now(), RecoverOptions{AccountID: other}); err != nil {
		t.Fatal(err)
	}
	var epoch string
	if err := db.QueryRow("SELECT account_epoch FROM auth_federated_subjects WHERE sub = ?", subject).Scan(&epoch); err != nil {
		t.Fatal(err)
	}
	if epoch != "2" {
		t.Fatalf("full recovery kept the account epoch: %s", epoch)
	}
	if err := ResetSchema(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	assertRows(t, db, "SELECT COUNT(*) FROM auth_oidc_clients", 0)
	assertRows(t, db, "SELECT COUNT(*) FROM auth_federated_subjects", 0)
	if _, err := s.Epochs(ctx, client.ID, subject); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old login survived the schema reset: %v", err)
	}
}
