package memo

import (
	"bytes"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"minimal-web-kit/internal/web"
)

//go:embed views/*.html
var viewsFS embed.FS

type Handler struct {
	store     *Store
	templates map[string]*template.Template
	logger    *slog.Logger
}

func NewHandler(s *Store, logger *slog.Logger) (*Handler, error) {
	tmplMap := make(map[string]*template.Template)

	pages := []string{"index.html", "detail.html", "confirm_delete.html"}
	for _, page := range pages {
		t, err := template.ParseFS(viewsFS, "views/layout.html", "views/"+page)
		if err != nil {
			return nil, fmt.Errorf("parse memo template %q: %w", page, err)
		}
		tmplMap[page] = t
	}

	return &Handler{
		store:     s,
		templates: tmplMap,
		logger:    logger,
	}, nil
}

func (h *Handler) render(w http.ResponseWriter, page string, data any) {
	tmpl, ok := h.templates[page]
	if !ok {
		h.logger.Error("memo template not found", slog.String("page", page))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		h.logger.Error("memo render failed", slog.String("page", page), slog.Any("error", err))
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

func (h *Handler) HandleList(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
	memos, err := h.store.ListMemos(r.Context(), ownerID)
	if err != nil {
		h.logger.Error("list memos failed", slog.String("owner_id", string(ownerID)), slog.Any("error", err))
		http.Error(w, "メモの読み込みに失敗しました", http.StatusInternalServerError)
		return
	}

	h.render(w, "index.html", map[string]any{
		"Title":     "プライベートメモ一覧",
		"Memos":     memos,
		"CSRFToken": web.GetCSRFToken(r.Context()),
	})
}

func (h *Handler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
	title := strings.TrimSpace(r.PostFormValue("title"))
	content := strings.TrimSpace(r.PostFormValue("content"))

	if title == "" || content == "" {
		http.Error(w, "タイトルと本文は必須です", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(title) > 100 {
		http.Error(w, "タイトルは100文字以内で入力してください", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(content) > 10000 {
		http.Error(w, "本文は10,000文字以内で入力してください", http.StatusBadRequest)
		return
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		http.Error(w, "ID生成に失敗しました", http.StatusInternalServerError)
		return
	}
	id := hex.EncodeToString(b)
	now := time.Now()

	m := &Memo{
		ID:        id,
		OwnerID:   ownerID,
		Title:     title,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := h.store.CreateMemo(r.Context(), m); err != nil {
		h.logger.Error("create memo failed", slog.Any("memo", m), slog.Any("error", err))
		http.Error(w, "メモの保存に失敗しました", http.StatusInternalServerError)
		return
	}

	h.logger.Info("memo created", slog.Any("memo", m))
	http.Redirect(w, r, "/memos", http.StatusSeeOther)
}

func (h *Handler) HandleDetail(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
	id := r.PathValue("id")

	m, err := h.store.GetMemo(r.Context(), ownerID, id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "メモが見つかりません", http.StatusNotFound)
		return
	}
	if err != nil {
		h.logger.Error("get memo failed", slog.String("id", id), slog.Any("error", err))
		http.Error(w, "メモの取得に失敗しました", http.StatusInternalServerError)
		return
	}

	h.render(w, "detail.html", map[string]any{
		"Title":     "メモの編集",
		"Memo":      m,
		"CSRFToken": web.GetCSRFToken(r.Context()),
	})
}

func (h *Handler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
	id := r.PathValue("id")
	title := strings.TrimSpace(r.PostFormValue("title"))
	content := strings.TrimSpace(r.PostFormValue("content"))

	if title == "" || content == "" {
		http.Error(w, "タイトルと本文は必須です", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(title) > 100 || utf8.RuneCountInString(content) > 10000 {
		http.Error(w, "文字数上限を超えています", http.StatusBadRequest)
		return
	}

	m := &Memo{
		ID:        id,
		OwnerID:   ownerID,
		Title:     title,
		Content:   content,
		UpdatedAt: time.Now(),
	}

	if err := h.store.UpdateMemo(r.Context(), m); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "メモが見つかりません", http.StatusNotFound)
			return
		}
		h.logger.Error("update memo failed", slog.Any("memo", m), slog.Any("error", err))
		http.Error(w, "メモの更新に失敗しました", http.StatusInternalServerError)
		return
	}

	h.logger.Info("memo updated", slog.Any("memo", m))
	http.Redirect(w, r, "/memos", http.StatusSeeOther)
}

func (h *Handler) HandleDeleteConfirm(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
	id := r.PathValue("id")

	m, err := h.store.GetMemo(r.Context(), ownerID, id)
	if errors.Is(err, ErrNotFound) {
		http.Error(w, "メモが見つかりません", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "メモの取得に失敗しました", http.StatusInternalServerError)
		return
	}

	h.render(w, "confirm_delete.html", map[string]any{
		"Title":     "メモ削除の確認",
		"Memo":      m,
		"CSRFToken": web.GetCSRFToken(r.Context()),
	})
}

func (h *Handler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
	id := r.PathValue("id")

	if err := h.store.DeleteMemo(r.Context(), ownerID, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "メモが見つかりません", http.StatusNotFound)
			return
		}
		h.logger.Error("delete memo failed", slog.String("id", id), slog.Any("error", err))
		http.Error(w, "メモの削除に失敗しました", http.StatusInternalServerError)
		return
	}

	h.logger.Info("memo deleted", slog.String("id", id))
	http.Redirect(w, r, "/memos", http.StatusSeeOther)
}

func (h *Handler) HandleExport(w http.ResponseWriter, r *http.Request) {
	ownerID := web.GetOwnerID(r.Context())
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
