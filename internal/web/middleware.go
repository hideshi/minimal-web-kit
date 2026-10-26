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
// クライアントからのヘッダー値に依存せず、常にサーバー側で12バイト（24文字Hex）の安全な乱数を生成し、
// ログ注入やトークン反射を防ぎます。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 12)
		_, _ = rand.Read(b)
		reqID := hex.EncodeToString(b)

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
					)
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// defaultCSP は全応答の既定のCSPです。JavaScriptは読み込ませません（script-src 'none'）。
const defaultCSP = "default-src 'self'; style-src 'self'; script-src 'none'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';"

// scriptCSP は、承認した画面だけで使うCSPです。defaultCSP との違いは script-src 'self' だけです。
const scriptCSP = "default-src 'self'; style-src 'self'; script-src 'self'; form-action 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'self';"

// AllowScripts は、この応答だけ自サイトのJSファイルを読み込めるようにします（インラインのJSと eval は許しません）。
// 部品のJS等を読み込む画面で、描画に成功した後、WriteHeader より前に呼びます。
// 既定のCSPのときだけ置き換え、機能が独自に設定したCSPは変えません。
func AllowScripts(w http.ResponseWriter) {
	if w.Header().Get("Content-Security-Policy") == defaultCSP {
		w.Header().Set("Content-Security-Policy", scriptCSP)
	}
}

// SecurityHeaders はセキュリティヘッダーを付与するミドルウェアです。
// JavaScriptを既定で使わないため、CSPの script-src は 'none' を指定します（承認した画面だけ AllowScripts で緩めます）。
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", defaultCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store, max-age=0")
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

// AccessLog はURL・入力値を含めず、固定分類と応答状況のみの構造化アクセスログを出力します。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			next.ServeHTTP(sw, r)

			duration := time.Since(start)
			reqID := w.Header().Get("X-Request-ID")

			// URLや任意のメソッド名に含まれる機密値を保存しない。
			method := r.Method
			switch method {
			case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
				http.MethodPatch, http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
			default:
				method = "OTHER"
			}

			logger.Info("http request",
				slog.String("request_id", reqID),
				slog.String("method", method),
				slog.Int("status", sw.statusCode),
				slog.Duration("duration", duration),
			)
		})
	}
}

// CSRFProtect は状態変更リクエスト（POST, PUT, DELETE等）に対してCSRFトークンを検証するミドルウェアです。
// トークンはPOST本文（PostFormValue）またはヘッダーからのみ取得し、URLクエリからの混入を拒否します。
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

			// フォーム本文の明示的解析
			if err := r.ParseForm(); err != nil {
				http.Error(w, "Bad Request: Failed to parse form", http.StatusBadRequest)
				return
			}

			// POST本文からCSRFトークンを取得（PostFormValueによりクエリ文字列を除外）
			token := r.PostFormValue("csrf_token")
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
