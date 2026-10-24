package app

import (
	"os"
	"time"
)

type Config struct {
	Addr         string
	DBPath       string
	SecureCookie bool
	GCSessionAge time.Duration
	GCInterval   time.Duration
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
}

func LoadConfig() *Config {
	addr := os.Getenv("ADDR")
	if addr == "" {
		port := os.Getenv("PORT")
		if port != "" {
			addr = "127.0.0.1:" + port
		} else {
			addr = "127.0.0.1:8080" // 安全なローカル待受を既定にする
		}
	}

	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "data/app.db"
	}

	secure := os.Getenv("SECURE_COOKIE") == "true"

	return &Config{
		Addr:         addr,
		DBPath:       dbPath,
		SecureCookie: secure,
		GCSessionAge: 30 * 24 * time.Hour,
		GCInterval:   1 * time.Hour,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}
