package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Addr, ControlAddr, DatabaseURL, RedisAddr, DirectRedisAddr, WebOrigin string
	ControlURL, APIURL, LoadgenURL, LoadTargetURL, ToxiproxyURL           string
	AdmissionSecret, Replica                                              string
	DBMaxConns, RedisPoolSize                                             int
	SweepInterval                                                         time.Duration
	LabEnabled                                                            bool
}

func Load() Config {
	host, _ := os.Hostname()
	return Config{
		Addr: env("API_ADDR", ":8080"), ControlAddr: env("CONTROL_ADDR", ":8081"),
		DatabaseURL:     env("DATABASE_URL", "postgres://flashseat:flashseat@localhost:5432/flashseat?sslmode=disable"),
		RedisAddr:       env("REDIS_ADDR", "localhost:6379"),
		DirectRedisAddr: env("DIRECT_REDIS_ADDR", env("REDIS_ADDR", "localhost:6379")),
		WebOrigin:       env("WEB_ORIGIN", "http://localhost:3000"),
		ControlURL:      env("CONTROL_URL", ""),
		APIURL:          env("API_URL", "http://api:8080"),
		LoadgenURL:      env("LOADGEN_URL", "http://localhost:8090"),
		LoadTargetURL:   env("LOAD_TARGET_URL", "http://gateway:8080/api/v1"),
		ToxiproxyURL:    env("TOXIPROXY_URL", ""),
		AdmissionSecret: env("ADMISSION_SECRET", "local-development-only"),
		Replica:         env("REPLICA", host),
		DBMaxConns:      integer("DB_MAX_CONNS", 32),
		RedisPoolSize:   integer("REDIS_POOL_SIZE", 128),
		SweepInterval:   time.Duration(integer("SWEEP_INTERVAL_MS", 500)) * time.Millisecond,
		LabEnabled:      env("LAB_ENABLED", "false") == "true",
	}
}

func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}

func integer(k string, fallback int) int {
	n, err := strconv.Atoi(env(k, strconv.Itoa(fallback)))
	if err != nil || n < 1 {
		return fallback
	}
	return n
}
