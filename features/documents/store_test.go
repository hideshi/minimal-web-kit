package documents

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web/page"
)

const (
	owner   = int64(1)
	alice   = int64(2)
	bob     = int64(3)
	stopped = int64(4)
	admin   = int64(5)
)

func newStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "docs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.DB().Exec(migrationSQL); err != nil {
		t.Fatal(err)
	}
	return NewStore(s.DB(), func() time.Time { return time.Unix(1_800_000_000, 0) }), s.DB()
}

func active(id int64) bool { return id != stopped }

var firstPage = page.Page{Number: 1, Size: 30}

func draft(t *testing.T, s *Store, by int64, title string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	id, err := s.Create(ctx, by, Input{Title: title, Body: "本文 " + title})
	if err != nil {
		t.Fatal(err)
	}
	_, v, err := s.Draft(ctx, by, id)
	if err != nil {
		t.Fatal(err)
	}
	return id, v.ID
}

func published(t *testing.T, s *Store, by int64, title, audience string, shares ...int64) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	id, v := draft(t, s, by, title)
	if err := s.Publish(ctx, by, id, v); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSharing(ctx, by, id, audience, shares, active); err != nil {
		t.Fatal(err)
	}
	return id, v
}

func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

func listed(t *testing.T, s *Store, viewer int64, f Filter) []string {
	t.Helper()
	items, err := s.List(context.Background(), viewer, f, firstPage)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Title)
	}
	return out
}

// 不変条件: DOC-INV-2、DOC-INV-4
// ユースケース: UC-DOCUMENTS-1, UC-DOCUMENTS-2
func TestRead_OnlyOwnerAndAudience(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	private, _ := published(t, s, owner, "秘密", audiencePrivate)
	shared, _ := published(t, s, owner, "限定", audienceSelected, alice)
	all, _ := published(t, s, owner, "全員", audienceEveryone)
	unpublished, _ := draft(t, s, owner, "下書き")
	if err := s.SetSharing(ctx, owner, unpublished, audienceEveryone, nil, active); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		viewer, id int64
		ok         bool
	}{
		{owner, private, true}, {alice, private, false}, {admin, private, false},
		{alice, shared, true}, {bob, shared, false}, {admin, shared, false},
		{bob, all, true}, {alice, unpublished, false},
	} {
		_, _, err := s.Current(ctx, c.viewer, c.id)
		if c.ok != (err == nil) {
			t.Errorf("viewer %d doc %d: %v", c.viewer, c.id, err)
		}
		if !c.ok {
			wantErr(t, err, ErrNotFound)
		}
	}
	if got := listed(t, s, bob, Filter{}); len(got) != 1 || got[0] != "全員" {
		t.Fatalf("bob sees %v", got)
	}
	// 検索も同じ条件を通る
	if got := listed(t, s, bob, Filter{Query: "秘密"}); len(got) != 0 {
		t.Fatalf("search leaked: %v", got)
	}
	if got := listed(t, s, alice, Filter{Query: "限定"}); len(got) != 1 {
		t.Fatalf("alice search: %v", got)
	}
	// LIKE の記号は文字として扱う
	if got := listed(t, s, alice, Filter{Query: "%"}); len(got) != 0 {
		t.Fatalf("wildcard matched: %v", got)
	}
}

// 不変条件: DOC-INV-2
// ユースケース: UC-DOCUMENTS-5
func TestRead_ApproverSeesOnlyTheRequestedVersion(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	id, first := published(t, s, owner, "規定", audienceSelected, alice)
	if err := s.Revise(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	_, v, err := s.Draft(ctx, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RequestApproval(ctx, owner, id, v.ID, []int64{bob}, active); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.VersionFor(ctx, bob, id, v.ID); err != nil {
		t.Fatalf("approver must read the requested version: %v", err)
	}
	// 共有先でない承認者に、公開中の版を見せない
	_, _, err = s.VersionFor(ctx, bob, id, first)
	wantErr(t, err, ErrNotFound)
	_, _, err = s.Current(ctx, bob, id)
	wantErr(t, err, ErrNotFound)
	// 共有先は依頼中の版を読めない
	_, _, err = s.VersionFor(ctx, alice, id, v.ID)
	wantErr(t, err, ErrNotFound)
	// 取り下げた版は、判断していない承認者には読めない
	if err := s.Withdraw(ctx, owner, id, v.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.VersionFor(ctx, bob, id, v.ID)
	wantErr(t, err, ErrNotFound)
	// 別の文書の版を混ぜても読めない
	other, _ := published(t, s, owner, "別", audienceEveryone)
	_, _, err = s.VersionFor(ctx, alice, other, first)
	wantErr(t, err, ErrNotFound)
}

// 不変条件: DOC-INV-3
// ユースケース: UC-DOCUMENTS-6
func TestOwnerOnly_OthersCannotChange(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	id, v := published(t, s, owner, "回覧", audienceEveryone)
	if err := s.Revise(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	_, d, _ := s.Draft(ctx, owner, id)
	for name, err := range map[string]error{
		"save":     s.SaveDraft(ctx, admin, id, d.ID, Input{Title: "乗っ取り"}),
		"publish":  s.Publish(ctx, admin, id, d.ID),
		"revise":   s.Revise(ctx, admin, id),
		"request":  s.RequestApproval(ctx, admin, id, d.ID, []int64{alice}, active),
		"withdraw": s.Withdraw(ctx, admin, id, d.ID),
		"share":    s.SetSharing(ctx, admin, id, audiencePrivate, nil, active),
		"delete":   s.Delete(ctx, admin, id),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Versions(ctx, alice, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("versions: %v", err)
	}
	if _, err := s.Reads(ctx, alice, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("reads: %v", err)
	}
	_ = v
}

// 不変条件: DOC-INV-4、DOC-INV-5
// ユースケース: UC-DOCUMENTS-6
func TestVersions_FrozenAndDraftHidden(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	id, first := published(t, s, owner, "初版", audienceEveryone)
	if err := s.Revise(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Revise(ctx, owner, id), ErrConflict) // 下書きは1つまで
	_, d, _ := s.Draft(ctx, owner, id)
	if err := s.SaveDraft(ctx, owner, id, d.ID, Input{Title: "第2版の案", Body: "新しい本文"}); err != nil {
		t.Fatal(err)
	}
	_, cur, err := s.Current(ctx, alice, id)
	if err != nil || cur.ID != first || cur.Title != "初版" {
		t.Fatalf("sharers must keep the published version: %+v %v", cur, err)
	}
	// 公開済みの版は保存の口からもSQLからも変わらない
	wantErr(t, s.SaveDraft(ctx, owner, id, first, Input{Title: "書き換え"}), ErrConflict)
	if _, err := db.Exec("UPDATE document_versions SET body = 'x' WHERE id = ?", first); err == nil || !strings.Contains(err.Error(), "frozen") {
		t.Fatalf("frozen version changed: %v", err)
	}
	// 依頼の後も固定される
	if err := s.RequestApproval(ctx, owner, id, d.ID, []int64{alice}, active); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.SaveDraft(ctx, owner, id, d.ID, Input{Title: "依頼中の変更"}), ErrConflict)
	wantErr(t, s.Revise(ctx, owner, id), ErrConflict) // 承認待ちの間は改定を始められない
}

// 不変条件: DOC-INV-6、DOC-INV-7
// ユースケース: UC-DOCUMENTS-4, UC-DOCUMENTS-5
func TestApproval_AllMustApprove(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	id, v := draft(t, s, owner, "規定")
	if err := s.SetSharing(ctx, owner, id, audienceEveryone, nil, active); err != nil {
		t.Fatal(err)
	}
	var input InputError
	if err := s.RequestApproval(ctx, owner, id, v, []int64{owner, alice}, active); !errors.As(err, &input) {
		t.Fatalf("the owner must not approve: %v", err)
	}
	if err := s.RequestApproval(ctx, owner, id, v, nil, active); !errors.As(err, &input) {
		t.Fatalf("approvers are required: %v", err)
	}
	if err := s.RequestApproval(ctx, owner, id, v, []int64{stopped}, active); !errors.As(err, &input) {
		t.Fatalf("stopped accounts must not be chosen: %v", err)
	}
	if err := s.RequestApproval(ctx, owner, id, v, []int64{alice, bob}, active); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Publish(ctx, owner, id, v), ErrConflict)
	wantErr(t, s.Approve(ctx, admin, id, v), ErrNotFound) // 選ばれていない人には、版の有無も伝えない
	if err := s.Approve(ctx, alice, id, v); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Approve(ctx, alice, id, v), ErrConflict) // 二重の承認で数えない
	if _, _, err := s.Current(ctx, admin, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("not published before everyone approved: %v", err)
	}
	if err := s.Approve(ctx, bob, id, v); err != nil {
		t.Fatal(err)
	}
	if _, cur, err := s.Current(ctx, admin, id); err != nil || cur.ID != v {
		t.Fatalf("published after everyone approved: %+v %v", cur, err)
	}
}

// 不変条件: DOC-INV-5、DOC-INV-6
// ユースケース: UC-DOCUMENTS-5, UC-DOCUMENTS-6
func TestApproval_RejectAndBypass(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	id, v := draft(t, s, owner, "規定")
	if err := s.RequestApproval(ctx, owner, id, v, []int64{alice, bob}, active); err != nil {
		t.Fatal(err)
	}
	var input InputError
	if err := s.Reject(ctx, alice, id, v, ""); !errors.As(err, &input) {
		t.Fatalf("a reason is required: %v", err)
	}
	if err := s.Reject(ctx, alice, id, v, "表現を直してください"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.Approve(ctx, bob, id, v), ErrConflict) // 差し戻しの後は判断できない
	_, d, err := s.Draft(ctx, owner, id)
	if err != nil || d.ID == v {
		t.Fatalf("a new draft must exist: %+v %v", d, err)
	}
	// 差し戻された内容を、承認なしで公開できない
	wantErr(t, s.Publish(ctx, owner, id, d.ID), ErrConflict)
	// 差し戻しの記録が指す版の内容は変わらない
	if err := s.SaveDraft(ctx, owner, id, d.ID, Input{Title: "直した規定", Body: "直した"}); err != nil {
		t.Fatal(err)
	}
	_, old, err := s.VersionFor(ctx, alice, id, v)
	if err != nil || old.Title != "規定" {
		t.Fatalf("rejected version changed: %+v %v", old, err)
	}
	// 承認済みの文書の改定も、承認を受け直す
	if err := s.RequestApproval(ctx, owner, id, d.ID, []int64{alice}, active); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(ctx, alice, id, d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Revise(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	_, next, _ := s.Draft(ctx, owner, id)
	wantErr(t, s.Publish(ctx, owner, id, next.ID), ErrConflict)
}

// 不変条件: DOC-INV-8
// ユースケース: UC-DOCUMENTS-3
func TestReads_PerVersionAndFirstOnly(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	id, _ := published(t, s, owner, "回覧", audienceEveryone)
	d, _, err := s.Current(ctx, alice, id)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(1_800_000_100, 0) }
	if err := s.RecordRead(ctx, d, alice); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(1_800_000_200, 0) }
	if err := s.RecordRead(ctx, d, alice); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordRead(ctx, d, owner); err != nil {
		t.Fatal(err)
	}
	reads, err := s.Reads(ctx, owner, id)
	if err != nil || len(reads) != 1 || reads[alice].Unix() != 1_800_000_100 {
		t.Fatalf("reads = %v, %v", reads, err)
	}
	if err := s.Revise(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	_, v, _ := s.Draft(ctx, owner, id)
	if err := s.Publish(ctx, owner, id, v.ID); err != nil {
		t.Fatal(err)
	}
	reads, err = s.Reads(ctx, owner, id)
	if err != nil || len(reads) != 0 {
		t.Fatalf("a new version must be unread: %v, %v", reads, err)
	}
	items, _ := s.List(ctx, alice, Filter{}, firstPage)
	if len(items) != 1 || !items[0].Unread {
		t.Fatalf("items = %+v", items)
	}
}

// 不変条件: DOC-INV-7、DOC-INV-9、DOC-INV-11
// ユースケース: UC-DOCUMENTS-8
func TestTransfer_InactiveOwnerOnly(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	id, v := draft(t, s, stopped, "承認待ち")
	if err := s.RequestApproval(ctx, stopped, id, v, []int64{alice}, func(int64) bool { return true }); err != nil {
		t.Fatal(err)
	}
	shared, _ := published(t, s, stopped, "共有", audienceSelected, alice)
	usable := func(from, to int64) (bool, error) { return from == stopped && active(to), nil }

	var input InputError
	if _, err := s.TransferOwner(ctx, admin, stopped, admin, usable); !errors.As(err, &input) {
		t.Fatalf("the operator must not take the documents: %v", err)
	}
	mine, _ := draft(t, s, alice, "有効な人の文書")
	if _, err := s.TransferOwner(ctx, admin, alice, bob, usable); !errors.Is(err, ErrConflict) {
		t.Fatalf("active owners must keep their documents: %v", err)
	}
	if _, err := s.Owned(ctx, alice, mine); err != nil {
		t.Fatalf("the active owner lost the document: %v", err)
	}
	if n, err := s.TransferOwner(ctx, admin, stopped, alice, usable); err != nil || n != 2 {
		t.Fatalf("transfer: %d %v", n, err)
	}
	// 新しい所有者は、元の依頼を自分で承認できない
	wantErr(t, s.Approve(ctx, alice, id, v), ErrConflict)
	if _, d, err := s.Draft(ctx, alice, id); err != nil || d.Title != "承認待ち" {
		t.Fatalf("draft = %+v %v", d, err)
	}
	var shares int
	if err := db.QueryRow("SELECT COUNT(*) FROM document_shares WHERE document_id = ?", shared).Scan(&shares); err != nil || shares != 0 {
		t.Fatalf("shares = %d %v", shares, err)
	}
	log, err := s.Transfers(ctx)
	if err != nil || len(log) != 1 || log[0].By != admin || log[0].Count != 2 {
		t.Fatalf("log = %+v %v", log, err)
	}
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if log, _ := s.Transfers(ctx); len(log) != 1 {
		t.Fatal("a recent record must stay")
	}
	s.now = func() time.Time { return time.Unix(1_800_000_000, 0).Add(transferRetention + time.Hour) }
	if err := s.Purge(ctx); err != nil {
		t.Fatal(err)
	}
	if log, _ := s.Transfers(ctx); len(log) != 0 {
		t.Fatal("an expired record must go")
	}
}

// ユースケース: UC-DOCUMENTS-7
func TestFolders_SharedAndProtected(t *testing.T) {
	ctx := context.Background()
	s, _ := newStore(t)
	if err := s.AddFolder(ctx, alice, "社内規定"); err != nil {
		t.Fatal(err)
	}
	var input InputError
	if err := s.AddFolder(ctx, bob, "社内規定"); !errors.As(err, &input) || input.Field != "name_taken" {
		t.Fatalf("duplicate: %v", err)
	}
	folders, _ := s.Folders(ctx)
	folder := folders[0].ID
	wantErr(t, s.RenameFolder(ctx, bob, false, folder, "別"), ErrNotFound)
	if err := s.RenameFolder(ctx, admin, true, folder, "規定"); err != nil {
		t.Fatal(err)
	}
	id, err := s.Create(ctx, owner, Input{Title: "読めない文書", FolderID: folder})
	if err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.DeleteFolder(ctx, alice, false, folder), ErrConflict) // 読めない文書も数える
	if err := s.Delete(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	wantErr(t, s.DeleteFolder(ctx, bob, false, folder), ErrNotFound)
	if err := s.DeleteFolder(ctx, alice, false, folder); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, owner, Input{Title: "x", FolderID: 999}); !errors.As(err, &input) {
		t.Fatalf("unknown folder: %v", err)
	}
}

// ユースケース: UC-DOCUMENTS-6
func TestDelete_RemovesEverything(t *testing.T) {
	ctx := context.Background()
	s, db := newStore(t)
	id, v := published(t, s, owner, "回覧", audienceSelected, alice)
	d, _, _ := s.Current(ctx, alice, id)
	if err := s.RecordRead(ctx, d, alice); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SELECT COUNT(*) FROM document_versions", "SELECT COUNT(*) FROM document_shares", "SELECT COUNT(*) FROM document_reads"} {
		var n int
		if err := db.QueryRow(q).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s = %d, %v", q, n, err)
		}
	}
	_ = v
}
