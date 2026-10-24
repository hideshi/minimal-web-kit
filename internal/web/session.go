package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	store      store.Store
	secureFlag bool
	secretKey  []byte
}

func NewSessionManager(s store.Store, secure bool) *SessionManager {
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
	expected := sm.GenerateCSRFToken(sessionToken)
	return hmac.Equal([]byte(expected), []byte(token))
}

// ResolveOwner はCookieからセッションを読み取り、OwnerIDとCSRFトークンをリクエストcontextに設定します。
// セッションが存在しない場合は新規に発行してCookieを設定します。
func (sm *SessionManager) ResolveOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var token string
		cookie, err := r.Cookie(sessionCookieName)
		if err == nil && cookie.Value != "" {
			token = cookie.Value
		}

		var tokenHash string
		if token != "" {
			h := sha256.Sum256([]byte(token))
			tokenHash = hex.EncodeToString(h[:])
		} else {
			// 新規セッショントークン生成（128bit以上）
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			token = hex.EncodeToString(b)
			h := sha256.Sum256([]byte(token))
			tokenHash = hex.EncodeToString(h[:])

			// Cookie設定
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
		}

		ownerID, err := sm.store.GetOrCreateSession(r.Context(), tokenHash)
		if err != nil {
			http.Error(w, "Session Error", http.StatusInternalServerError)
			return
		}

		csrfToken := sm.GenerateCSRFToken(token)

		ctx := context.WithValue(r.Context(), ownerIDContextKey, ownerID)
		ctx = context.WithValue(ctx, csrfTokenContextKey, csrfToken)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
