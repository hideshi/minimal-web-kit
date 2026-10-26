// 比較試作の明示起動専用。通常のcmd/serverには組み込みません。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	catalogtags "minimal-web-kit/experiments/catalog-tags"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("comparison server failed")
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	css, err := os.ReadFile("internal/web/static/css/style.css")
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	origin := "http://" + listener.Addr().String()
	handler, err := catalogtags.NewHandler(css, origin, logger)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdown)
		case <-done:
		}
	}()
	fmt.Println("標準HTML: " + origin + "/compare?mode=html&count=50")
	fmt.Println("Alpine.js（一覧の絞り込み）: " + origin + "/compare?mode=alpine&count=50")
	fmt.Println("Alpine.js（コンボボックス）: " + origin + "/compare?mode=combo&count=50")
	fmt.Println("素のJS（コンボボックス）: " + origin + "/compare?mode=vanilla&count=50")
	fmt.Println("比較専用・架空データのみ。Ctrl+Cで終了します。")
	err = srv.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
