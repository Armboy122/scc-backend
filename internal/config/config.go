package config

import (
	"os"
	"strconv"
	"time"
)

// Config holds all application configuration loaded from environment variables.
type Config struct {
	DatabaseURL string
	Port        string
	Env         string
	SeedData    bool
	AutoMigrate bool

	JWTSecret     string
	JWTAccessTTL  time.Duration
	JWTRefreshTTL time.Duration

	MinioEndpoint  string
	MinioAccessKey string
	MinioSecretKey string
	MinioBucket    string
	MinioPublicURL string
	MinioUseSSL    bool

	CORSOrigins string
}

// Load reads environment variables and returns a Config with sane defaults.
func Load() *Config {
	return &Config{
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://smartcover:smartcover@scc-postgres:5432/smartcover?sslmode=disable"),
		Port:           getEnv("PORT", "8080"),
		Env:            getEnv("ENV", "development"),
		SeedData:       getBoolEnv("SEED_DATA", false),
		AutoMigrate:    getBoolEnv("AUTO_MIGRATE", getEnv("ENV", "development") != "production"),
		JWTSecret:      getEnv("JWT_SECRET", "change-me-in-production"),
		JWTAccessTTL:   getDurationEnv("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL:  getDurationEnv("JWT_REFRESH_TTL", 168*time.Hour),
		MinioEndpoint:  getEnv("MINIO_ENDPOINT", "scc-minio:9000"),
		MinioAccessKey: getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinioSecretKey: getEnv("MINIO_SECRET_KEY", "minioadmin"),
		MinioBucket:    getEnv("MINIO_BUCKET", "scc"),
		MinioPublicURL: getEnv("MINIO_PUBLIC_URL", "http://localhost:9000"),
		MinioUseSSL:    getBoolEnv("MINIO_USE_SSL", false),
		CORSOrigins:    getEnv("CORS_ORIGINS", "http://localhost:3000"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getBoolEnv(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func getDurationEnv(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
