package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr, DatabaseURL, RedisAddr, WebOrigin string
	HoldTTL, SweepInterval                  time.Duration
}

func Load() Config {
	return Config{
		Addr: env("API_ADDR", ":8080"), DatabaseURL: env("DATABASE_URL", "postgres://flashseat:flashseat@localhost:5432/flashseat?sslmode=disable"),
		RedisAddr: env("REDIS_ADDR", "localhost:6379"), WebOrigin: env("WEB_ORIGIN", "http://localhost:3000"),
		HoldTTL: seconds("HOLD_TTL_SECONDS", 600), SweepInterval: seconds("SWEEP_INTERVAL_SECONDS", 2),
	}
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
func seconds(k string, fallback int) time.Duration {
	n, err := strconv.Atoi(env(k, strconv.Itoa(fallback)))
	if err != nil || n < 1 {
		n = fallback
	}
	return time.Duration(n) * time.Second
}
