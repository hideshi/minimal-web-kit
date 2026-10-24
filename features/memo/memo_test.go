package memo_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"minimal-web-kit/features/memo"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"

	_ "modernc.org/sqlite"
)

// TestMemo_CrossOwnerIsolation はメモ機能における他者データへのアクセス拒否とログ保護を検証します。
func TestMemo_CrossOwnerIsolation(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	// インメモリSQLiteDBの準備
	sqliteStore, err := store.NewSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer sqliteStore.Close()

	// Memo機能の初期化（同じDBハンドルを使用）
	feat := memo.NewFeature(logger)
	if err := feat.Init(sqliteStore.DB()); err != nil {
		t.Fatalf("feat.Init: %v", err)
	}

	sm := web.NewSessionManager(sqliteStore, false)
	coreHandler, err := web.NewHandler(sqliteStore, sm, logger)
	if err != nil {
		t.Fatalf("create core handler: %v", err)
	}

	// ルーティング合成
	routes := coreHandler.Routes(func(mux *http.ServeMux, sm *web.SessionManager) {
		feat.RegisterRoutes(mux, sm)
	})

	// 1. ユーザーAのセッション確立
	recA := httptest.NewRecorder()
	routes.ServeHTTP(recA, httptest.NewRequest(http.MethodGet, "/", nil))
	cookieA := recA.Result().Cookies()[0]
	csrfTokenA := sm.GenerateCSRFToken(cookieA.Value)

	// 2. ユーザーAが全角日本語タイトル＋機密カナリア文字列でメモを作成
	canarySecret := "CANARY_SECRET_MEMO_FEATURE_12345"
	titleA := "ユーザーAの機密メモ"
	formA := url.Values{}
	formA.Set("csrf_token", csrfTokenA)
	formA.Set("title", titleA)
	formA.Set("content", canarySecret)

	reqCreate := httptest.NewRequest(http.MethodPost, "/memos", strings.NewReader(formA.Encode()))
	reqCreate.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqCreate.AddCookie(cookieA)
	recCreate := httptest.NewRecorder()
	routes.ServeHTTP(recCreate, reqCreate)

	if recCreate.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d. Body: %s", recCreate.Code, recCreate.Body.String())
	}

	// 3. ログにカナリア文字列が出現していないこと
	if strings.Contains(logBuf.String(), canarySecret) {
		t.Fatalf("CRITICAL SECURITY FAILURE: Canary secret was leaked into logs!")
	}

	// 4. メモIDを取得
	reqExportA := httptest.NewRequest(http.MethodGet, "/memos/export", nil)
	reqExportA.AddCookie(cookieA)
	recExportA := httptest.NewRecorder()
	routes.ServeHTTP(recExportA, reqExportA)

	var exportA struct {
		Count int `json:"count"`
		Memos []struct {
			ID      string `json:"id"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"memos"`
	}
	body, _ := io.ReadAll(recExportA.Body)
	_ = json.Unmarshal(body, &exportA)
	if exportA.Count != 1 {
		t.Fatalf("expected 1 memo, got %d", exportA.Count)
	}
	memoID := exportA.Memos[0].ID

	// 5. ユーザーBのセッション確立
	recB := httptest.NewRecorder()
	routes.ServeHTTP(recB, httptest.NewRequest(http.MethodGet, "/", nil))
	cookieB := recB.Result().Cookies()[0]
	csrfTokenB := sm.GenerateCSRFToken(cookieB.Value)

	// 6. クロスオーナーアクセスの拒否検証
	// (a) ユーザーBのメモ一覧にユーザーAのメモが出現しないこと
	reqListB := httptest.NewRequest(http.MethodGet, "/memos", nil)
	reqListB.AddCookie(cookieB)
	recListB := httptest.NewRecorder()
	routes.ServeHTTP(recListB, reqListB)
	if strings.Contains(recListB.Body.String(), titleA) {
		t.Errorf("CRITICAL SECURITY FAILURE: User A's memo appeared in User B's list!")
	}

	// (b) ユーザーBがユーザーAのメモを取得しようとすると 404
	reqGetCross := httptest.NewRequest(http.MethodGet, "/memos/"+memoID, nil)
	reqGetCross.AddCookie(cookieB)
	recGetCross := httptest.NewRecorder()
	routes.ServeHTTP(recGetCross, reqGetCross)
	if recGetCross.Code != http.StatusNotFound {
		t.Errorf("expected 404 for cross-owner GET, got %d", recGetCross.Code)
	}

	// (c) ユーザーBがユーザーAのメモを更新しようとすると 404
	formUpdateCross := url.Values{}
	formUpdateCross.Set("csrf_token", csrfTokenB)
	formUpdateCross.Set("title", "不正更新")
	formUpdateCross.Set("content", "不正更新")
	reqUpdateCross := httptest.NewRequest(http.MethodPost, "/memos/"+memoID, strings.NewReader(formUpdateCross.Encode()))
	reqUpdateCross.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqUpdateCross.AddCookie(cookieB)
	recUpdateCross := httptest.NewRecorder()
	routes.ServeHTTP(recUpdateCross, reqUpdateCross)
	if recUpdateCross.Code != http.StatusNotFound {
		t.Errorf("expected 404 for cross-owner POST update, got %d", recUpdateCross.Code)
	}

	// (d) ユーザーBがユーザーAのメモを削除しようとすると 404
	formDelCross := url.Values{}
	formDelCross.Set("csrf_token", csrfTokenB)
	reqDelCross := httptest.NewRequest(http.MethodPost, "/memos/"+memoID+"/delete", strings.NewReader(formDelCross.Encode()))
	reqDelCross.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDelCross.AddCookie(cookieB)
	recDelCross := httptest.NewRecorder()
	routes.ServeHTTP(recDelCross, reqDelCross)
	if recDelCross.Code != http.StatusNotFound {
		t.Errorf("expected 404 for cross-owner POST delete, got %d", recDelCross.Code)
	}

	// 7. CASCADE削除の検証: ユーザーAがアカウント全データ削除を実行すると、memosテーブルからも自動で消えること！
	formDelAll := url.Values{}
	formDelAll.Set("csrf_token", csrfTokenA)
	reqDelAll := httptest.NewRequest(http.MethodPost, "/account/delete-all", strings.NewReader(formDelAll.Encode()))
	reqDelAll.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqDelAll.AddCookie(cookieA)
	recDelAll := httptest.NewRecorder()
	routes.ServeHTTP(recDelAll, reqDelAll)

	if recDelAll.Code != http.StatusSeeOther {
		t.Fatalf("expected 303, got %d", recDelAll.Code)
	}

	// 削除されたことを確認（直接DBをクエリ）
	var count int
	_ = sqliteStore.DB().QueryRow("SELECT COUNT(*) FROM memos WHERE id = ?", memoID).Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 memos after cascade deletion, got %d", count)
	}
}
