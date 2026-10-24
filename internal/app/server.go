package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"minimal-web-kit/features"
	"minimal-web-kit/internal/store"
	"minimal-web-kit/internal/web"
)

type Server struct {
	cfg      *Config
	store    store.SessionStore
	features *features.Registry
	logger   *slog.Logger
}

func NewServer(cfg *Config, s store.SessionStore, reg *features.Registry, logger *slog.Logger) *Server {
	return &Server{
		cfg:      cfg,
		store:    s,
		features: reg,
		logger:   logger,
	}
}

func (s *Server) Run() error {
	// 各機能の初期化（DBマイグレーション等）
	if s.features != nil && s.store.DB() != nil {
		for _, f := range s.features.All() {
			if err := f.Init(s.store.DB()); err != nil {
				return fmt.Errorf("init feature %s: %w", f.Name(), err)
			}
			s.logger.Info("feature initialized", slog.String("feature", f.Name()))
		}
	}

	// 起動時GC: 停止中に期限切れとなった放置セッションを一掃
	if count, err := s.store.PurgeExpiredSessions(context.Background(), s.cfg.GCSessionAge); err != nil {
		s.logger.Error("startup gc expired sessions failed", slog.Any("error", err))
	} else if count > 0 {
		s.logger.Info("startup gc expired sessions purged", slog.Int64("count", count))
	}

	sm := web.NewSessionManager(s.store, s.cfg.SecureCookie)
	handler, err := web.NewHandler(s.store, sm, s.logger)
	if err != nil {
		return fmt.Errorf("init handler: %w", err)
	}

	// ルートパス（GET /）の差し替え判定
	var rootHandler http.HandlerFunc
	if s.features != nil {
		for _, f := range s.features.All() {
			if cr, ok := f.(features.CustomRootFeature); ok {
				rootHandler = cr.HandleRoot
				break
			}
		}
	}

	// ルーティング構築
	httpHandler := handler.Routes(rootHandler, func(mux *http.ServeMux, sm *web.SessionManager) {
		if s.features != nil {
			for _, f := range s.features.All() {
				f.RegisterRoutes(mux, sm)
			}
		}
	})

	httpServer := &http.Server{
		Addr:         s.cfg.Addr,
		Handler:      httpHandler,
		ReadTimeout:  s.cfg.ReadTimeout,
		WriteTimeout: s.cfg.WriteTimeout,
		IdleTimeout:  s.cfg.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup

	// 放置セッションGCゴルーチンの起動
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(s.cfg.GCInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				count, err := s.store.PurgeExpiredSessions(context.Background(), s.cfg.GCSessionAge)
				if err != nil {
					s.logger.Error("gc expired sessions failed", slog.Any("error", err))
				} else if count > 0 {
					s.logger.Info("gc expired sessions purged", slog.Int64("count", count))
				}
			}
		}
	}()

	// HTTPサーバーの非同期リッスン
	serverErr := make(chan error, 1)
	go func() {
		s.logger.Info("server starting", slog.String("addr", s.cfg.Addr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	select {
	case err := <-serverErr:
		return fmt.Errorf("server error: %w", err)
	case <-ctx.Done():
		s.logger.Info("shutdown signal received, closing gracefully...")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		s.logger.Error("server shutdown error", slog.Any("error", err))
	}

	wg.Wait()
	s.logger.Info("server stopped successfully")
	return nil
}
