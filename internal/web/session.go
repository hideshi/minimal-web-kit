package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"time"

	"minimal-web-kit/internal/domain"
	"minimal-web-kit/internal/store"
)

type contextKey string

const (
	ownerIDContextKey   contextKey = "ownerID"
	csrfTokenContextKey contextKey = "csrfToken"
	sessionCookieName              = "session"
)

// SessionManager は匿名Cookieセッションの管理とOwnerIDの解決を担当します。
type SessionManager struct {
	store      store.SessionStore
	secureFlag bool
	secretKey  []byte
}

func NewSessionManager(s store.SessionStore, secure bool) *SessionManager {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		panic(fmt.Sprintf("init secret key: %v", err))
	}
	return &SessionManager{
		store:      s,
		secureFlag: secure,
		secretKey:  secret,
	}
}

// GenerateCSRFToken はセッショントークンと秘密鍵からCSRFトークンを導出します。
func (sm *SessionManager) GenerateCSRFToken(sessionToken string) string {
	mac := hmac.New(sha256.New, sm.secretKey)
	mac.Write([]byte(sessionToken))
	return hex.EncodeToString(mac.Sum(nil))
}

// ValidateCSRFToken は渡されたCSRFトークンが正当か検証します。
func (sm *SessionManager) ValidateCSRFToken(sessionToken, token string) bool {
	if sessionToken == "" || token == "" {
		return false
	}
	expected := sm.GenerateCSRFToken(sessionToken)
	return hmac.Equal([]byte(expected), []byte(token))
}

// ResolveOwner はCookieが存在する場合にのみOwnerIDを解決し、コンテキストに設定します。
// Cookieが存在しない場合や公開ページへのアクセスでは、DB行の作成やSet-Cookieは一切行いません。
// 未知・不正・失効済みのCookieが送られた場合はCookieを削除し、固定化攻撃を防ぎます。
func (sm *SessionManager) ResolveOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || cookie.Value == "" {
			// Cookieが存在しない場合はセッションを自動作成せずそのまま通す（Cookie不要・DB不要を保証）
			next.ServeHTTP(w, r)
			return
		}

		h := sha256.Sum256([]byte(cookie.Value))
		tokenHash := hex.EncodeToString(h[:])

		ownerID, err := sm.store.GetSessionOwner(r.Context(), tokenHash)
		if errors.Is(err, store.ErrNotFound) {
			// 未知・失効済みCookie: ブラウザからCookieを消去し、セッションなしとして通す
			sm.ClearSessionCookie(w)
			next.ServeHTTP(w, r)
			return
		} else if err != nil {
			http.Error(w, "Session Error", http.StatusInternalServerError)
			return
		}

		csrfToken := sm.GenerateCSRFToken(cookie.Value)
		ctx := context.WithValue(r.Context(), ownerIDContextKey, ownerID)
		ctx = context.WithValue(ctx, csrfTokenContextKey, csrfToken)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// EnsureSession はセッションが存在しない場合にのみ新規発行し、Cookieを設定してOwnerIDを返します。
func (sm *SessionManager) EnsureSession(w http.ResponseWriter, r *http.Request) (domain.OwnerID, string, error) {
	if ownerID := GetOwnerID(r.Context()); ownerID != "" {
		cookie, _ := r.Cookie(sessionCookieName)
		csrfToken := GetCSRFToken(r.Context())
		if cookie != nil {
			return ownerID, csrfToken, nil
		}
	}

	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	h := sha256.Sum256([]byte(token))
	tokenHash := hex.EncodeToString(h[:])

	ownerBytes := make([]byte, 16)
	if _, err := rand.Read(ownerBytes); err != nil {
		return "", "", fmt.Errorf("generate owner: %w", err)
	}
	ownerID := domain.OwnerID(hex.EncodeToString(ownerBytes))

	if err := sm.store.CreateSession(r.Context(), tokenHash, ownerID); err != nil {
		return "", "", fmt.Errorf("create session in store: %w", err)
	}

	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   sm.secureFlag,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   30 * 24 * 60 * 60, // 30日
	}
	http.SetCookie(w, c)

	csrfToken := sm.GenerateCSRFToken(token)
	return ownerID, csrfToken, nil
}

// GetOwnerID はコンテキストからOwnerIDを取り出します。
func GetOwnerID(ctx context.Context) domain.OwnerID {
	if val, ok := ctx.Value(ownerIDContextKey).(domain.OwnerID); ok {
		return val
	}
	return ""
}

// GetCSRFToken はコンテキストからCSRFトークンを取り出します。
func GetCSRFToken(ctx context.Context) string {
	if val, ok := ctx.Value(csrfTokenContextKey).(string); ok {
		return val
	}
	return ""
}

// ClearSessionCookie はセッションCookieを破棄します。
func (sm *SessionManager) ClearSessionCookie(w http.ResponseWriter) {
	c := &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   sm.secureFlag,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	}
	http.SetCookie(w, c)
}
