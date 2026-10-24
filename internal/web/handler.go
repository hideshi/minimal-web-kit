package web

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"minimal-web-kit/internal/domain"
	"minimal-web-kit/internal/store"
)

//go:embed views/*.html static/*
var contentFS embed.FS

type Handler struct {
	store     store.Store
	sm        *SessionManager
	templates map[string]*template.Template
	logger    *slog.Logger
}

func NewHandler(s store.Store, sm *SessionManager, logger *slog.Logger) (*Handler, error) {
	tmplMap := make(map[string]*template.Template)

	pages := []string{"index.html", "detail.html", "privacy.html"}
	for _, page := range pages {
		t, err := template.ParseFS(contentFS, "views/layout.html", "views/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse template %q: %w", page, err)
		}
		tmplMap[page] = t
	}

	return &Handler{
		store:     s,
		sm:        sm,
		templates: tmplMap,
		logger:    logger,
	}, nil
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()

	// 静的アセット配信
	staticFS, err := fs.Sub(contentFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))

	// アプリケーションルート
	mux.HandleFunc("GET /{$}", h.handleIndex)
	mux.HandleFunc("POST /memos", h.handleCreateMemo)
	mux.HandleFunc("GET /memos/{id}", h.handleDetailMemo)
	mux.HandleFunc("POST /memos/{id}", h.handleUpdateMemo)
	mux.HandleFunc("POST /memos/{id}/delete", h.handleDeleteMemo)
	mux.HandleFunc("GET /export", h.handleExport)
	mux.HandleFunc("POST /account/delete-all", h.handleDeleteAll)
	mux.HandleFunc("GET /privacy", h.handlePrivacy)

	// ミドルウェアスタックの合成
	// 外側から: RequestID -> Recoverer -> SecurityHeaders -> MaxBytes -> AccessLog -> ResolveOwner -> CSRFProtect
	handler := CSRFProtect(h.sm)(mux)
	handler = h.sm.ResolveOwner(handler)
	handler = AccessLog(h.logger)(handler)
	handler = MaxBytes(1024 * 1024)(handler) // 1MB制限
	handler = SecurityHeaders(handler)
	handler = Recoverer(h.logger)(handler)
	handler = RequestID(handler)

	return handler
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	memos, err := h.store.ListMemos(r.Context(), ownerID)
	if err != nil {
		h.logger.Error("list memos failed", slog.String("owner_id", string(ownerID)), slog.Any("error", err))
		http.Error(w, "メモの読み込みに失敗しました", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":     "プライベートメモ一覧",
		"Memos":     memos,
		"CSRFToken": GetCSRFToken(r.Context()),
	}

	if err := h.templates["index.html"].ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render index failed", slog.Any("error", err))
	}
}

func (h *Handler) handleCreateMemo(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	title := strings.TrimSpace(r.FormValue("title"))
	content := strings.TrimSpace(r.FormValue("content"))

	if title == "" || content == "" {
		http.Error(w, "タイトルと本文は必須です", http.StatusBadRequest)
		return
	}
	if len(title) > 100 {
		http.Error(w, "タイトルは100文字以内で入力してください", http.StatusBadRequest)
		return
	}
	if len(content) > 10000 {
		http.Error(w, "本文は10,000文字以内で入力してください", http.StatusBadRequest)
		return
	}

	// ランダムID生成
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "ID生成に失敗しました", http.StatusInternalServerError)
		return
	}
	id := hex.EncodeToString(b)
	now := time.Now()

	memo := &domain.Memo{
		ID:        id,
		OwnerID:   ownerID,
		Title:     title,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateMemo(r.Context(), memo); err != nil {
		h.logger.Error("create memo failed", slog.Any("memo", memo), slog.Any("error", err))
		http.Error(w, "メモの保存に失敗しました", http.StatusInternalServerError)
		return
	}

	// ログ契約: 本文は伏せてIDと長さのみログ出力
	h.logger.Info("memo created", slog.Any("memo", memo))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) handleDetailMemo(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	id := r.PathValue("id")

	memo, err := h.store.GetMemo(r.Context(), ownerID, id)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "メモが見つかりません", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("get memo failed", slog.String("id", id), slog.Any("error", err))
		http.Error(w, "メモの取得に失敗しました", http.StatusInternalServerError)
		return
	}

	data := map[string]any{
		"Title":     "メモの編集",
		"Memo":      memo,
		"CSRFToken": GetCSRFToken(r.Context()),
	}

	if err := h.templates["detail.html"].ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render detail failed", slog.Any("error", err))
	}
}

func (h *Handler) handleUpdateMemo(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	id := r.PathValue("id")
	title := strings.TrimSpace(r.FormValue("title"))
	content := strings.TrimSpace(r.FormValue("content"))

	if title == "" || content == "" {
		http.Error(w, "タイトルと本文は必須です", http.StatusBadRequest)
		return
	}
	if len(title) > 100 || len(content) > 10000 {
		http.Error(w, "文字数上限を超えています", http.StatusBadRequest)
		return
	}

	memo := &domain.Memo{
		ID:        id,
		OwnerID:   ownerID,
		Title:     title,
		Content:   content,
		UpdatedAt: time.Now(),
	}

	if err := h.store.UpdateMemo(r.Context(), memo); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "メモが見つかりません", http.StatusNotFound)
			return
		}
		h.logger.Error("update memo failed", slog.Any("memo", memo), slog.Any("error", err))
		http.Error(w, "メモの更新に失敗しました", http.StatusInternalServerError)
		return
	}

	h.logger.Info("memo updated", slog.Any("memo", memo))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) handleDeleteMemo(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	id := r.PathValue("id")

	if err := h.store.DeleteMemo(r.Context(), ownerID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "メモが見つかりません", http.StatusNotFound)
			return
		}
		h.logger.Error("delete memo failed", slog.String("id", id), slog.Any("error", err))
		http.Error(w, "メモの削除に失敗しました", http.StatusInternalServerError)
		return
	}

	h.logger.Info("memo deleted", slog.String("id", id))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) handleExport(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	memos, err := h.store.ListMemos(r.Context(), ownerID)
	if err != nil {
		http.Error(w, "エクスポートに失敗しました", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=memos-export.json")

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(map[string]any{
		"exported_at": time.Now().Format(time.RFC3339),
		"count":       len(memos),
		"memos":       memos,
	})
}

func (h *Handler) handleDeleteAll(w http.ResponseWriter, r *http.Request) {
	ownerID := GetOwnerID(r.Context())
	if err := h.store.DeleteAllData(r.Context(), ownerID); err != nil {
		h.logger.Error("delete all data failed", slog.String("owner_id", string(ownerID)), slog.Any("error", err))
		http.Error(w, "データ削除に失敗しました", http.StatusInternalServerError)
		return
	}

	h.sm.ClearSessionCookie(w)
	h.logger.Info("user data completely purged", slog.String("owner_id", string(ownerID)))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *Handler) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	data := map[string]any{
		"Title":     "このアプリが保存するもの",
		"CSRFToken": GetCSRFToken(r.Context()),
	}

	if err := h.templates["privacy.html"].ExecuteTemplate(w, "layout", data); err != nil {
		h.logger.Error("render privacy failed", slog.Any("error", err))
	}
}
