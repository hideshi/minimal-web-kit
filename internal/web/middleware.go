package web

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"
)

type statusResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *statusResponseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// RequestID は各リクエストにユニークな追跡IDを付与するミドルウェアです。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := r.Header.Get("X-Request-ID")
		if reqID == "" {
			b := make([]byte, 12)
			_, _ = rand.Read(b)
			reqID = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", reqID)
		next.ServeHTTP(w, r)
	})
}

// Recoverer はpanicをキャッチして500を返し、ログに記録するミドルウェアです。
func Recoverer(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rvr := recover(); rvr != nil {
					reqID := w.Header().Get("X-Request-ID")
					logger.Error("panic recovered",
						slog.String("request_id", reqID),
						slog.Any("error", rvr),
					)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders はセキュリティヘッダーを付与するミドルウェアです。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// MaxBytes はリクエスト本文のサイズを制限するミドルウェアです（DoS防止）。
func MaxBytes(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// AccessLog は個人データ（IP、User-Agent、Cookie、クエリ文字列）を伏せた安全な構造化アクセスログを出力します。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			reqID := w.Header().Get("X-Request-ID")

			// ログ契約: IP、User-Agent、Cookie、クエリパラメータ、メモ本文は一切出力しない
			logger.Info("http request",
				slog.String("request_id", reqID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.statusCode),
				slog.Duration("duration", duration),
			)
		})
	}
}

// CSRFProtect は状態変更リクエスト（POST, PUT, DELETE等）に対してCSRFトークンを検証するミドルウェアです。
func CSRFProtect(sm *SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// GET, HEAD, OPTIONS は安全なメソッドとして通過
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}

			// Sec-Fetch-Site が 'cross-site' の場合は即座に拒否
			if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
				http.Error(w, "Cross-site request blocked", http.StatusForbidden)
				return
			}

			// Cookieからセッショントークンを取得
			cookie, err := r.Cookie(sessionCookieName)
			if err != nil || cookie.Value == "" {
				http.Error(w, "Forbidden: Missing session", http.StatusForbidden)
				return
			}

			// フォームまたはヘッダーからCSRFトークンを取得
			token := r.FormValue("csrf_token")
			if token == "" {
				token = r.Header.Get("X-CSRF-Token")
			}

			if !sm.ValidateCSRFToken(cookie.Value, token) {
				http.Error(w, "Forbidden: Invalid CSRF Token", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
