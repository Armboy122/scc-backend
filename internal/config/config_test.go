package config

import (
	"strings"
	"testing"
	"time"
)

func validProductionConfig() *Config {
	return &Config{
		Env:                   "production",
		SeedData:              false,
		AutoMigrate:           false,
		JWTSecret:             "production-jwt-secret-with-at-least-32-bytes",
		JWTAccessTTL:          15 * time.Minute,
		JWTRefreshTTL:         7 * 24 * time.Hour,
		CORSOrigins:           "https://app.example.com",
		MinioInternalEndpoint: "scc-minio:9000",
		MinioPublicEndpoint:   "storage.example.com",
		MinioInternalUseSSL:   false,
		MinioPublicUseSSL:     true,
		MinioAccessKey:        "access",
		MinioSecretKey:        "secret",
		MinioBucket:           "scc",
		RunBackgroundJobs:     true,
		runBackgroundJobsRaw:  "true",
	}
}

func TestLoad_DefaultRefreshTTLIsSevenDays(t *testing.T) {
	t.Setenv("JWT_REFRESH_TTL", "")

	cfg := Load()

	if cfg.JWTRefreshTTL != 168*time.Hour {
		t.Fatalf("expected default refresh TTL 168h, got %s", cfg.JWTRefreshTTL)
	}
}

func TestLoad_Phase2BorrowingFlagDefaultsFalseAndParsesStrictValues(t *testing.T) {
	t.Setenv("ENABLE_PHASE2_BORROWING", "")
	cfg := Load()
	if cfg.EnablePhase2Borrowing {
		t.Fatal("phase 2 borrowing must default to disabled")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("absent flag should validate: %v", err)
	}

	t.Setenv("ENABLE_PHASE2_BORROWING", "true")
	cfg = Load()
	if !cfg.EnablePhase2Borrowing {
		t.Fatal("exact true must enable phase 2 borrowing")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("true flag should validate: %v", err)
	}

	t.Setenv("ENABLE_PHASE2_BORROWING", "false")
	cfg = Load()
	if cfg.EnablePhase2Borrowing {
		t.Fatal("exact false must disable phase 2 borrowing")
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("false flag should validate: %v", err)
	}
}

func TestConfigValidate_RejectsAmbiguousPhase2BorrowingFlag(t *testing.T) {
	for _, value := range []string{"TRUE", "1", " true ", "yes"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("ENABLE_PHASE2_BORROWING", value)
			cfg := Load()
			if cfg.EnablePhase2Borrowing {
				t.Fatalf("ambiguous value %q must fail closed", value)
			}
			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "ENABLE_PHASE2_BORROWING") {
				t.Fatalf("Validate() = %v, want phase 2 flag error", err)
			}
		})
	}
}

func TestLoad_RunBackgroundJobsDefaultsAndProductionExplicitness(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("RUN_BACKGROUND_JOBS", "")
	if cfg := Load(); !cfg.RunBackgroundJobs {
		t.Fatal("background jobs should default on outside production")
	}

	t.Setenv("ENV", "production")
	t.Setenv("RUN_BACKGROUND_JOBS", "")
	cfg := Load()
	if cfg.RunBackgroundJobs {
		t.Fatal("background jobs must fail closed when production flag is absent")
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "RUN_BACKGROUND_JOBS") {
		t.Fatalf("Validate() = %v, want explicit production background-jobs error", err)
	}

	for _, value := range []string{"true", "false"} {
		t.Run(value, func(t *testing.T) {
			candidate := validProductionConfig()
			candidate.runBackgroundJobsRaw = value
			candidate.RunBackgroundJobs = value == "true"
			if err := candidate.Validate(); err != nil {
				t.Fatalf("Validate() = %v", err)
			}
		})
	}
}

func TestConfigValidate_StrictBooleanAndDurationParsing(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(*Config)
		want string
	}{
		{name: "background bool", set: func(c *Config) { c.runBackgroundJobsRaw = "TRUE" }, want: "RUN_BACKGROUND_JOBS"},
		{name: "auto migrate bool", set: func(c *Config) { c.autoMigrateRaw = "1" }, want: "AUTO_MIGRATE"},
		{name: "access duration", set: func(c *Config) { c.jwtAccessTTLRaw = "fifteen" }, want: "JWT_ACCESS_TTL"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validProductionConfig()
			tt.set(candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want %s error", err, tt.want)
			}
		})
	}
}

func TestConfigValidate_ProductionSecurityPolicy(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "default JWT secret", mutate: func(c *Config) { c.JWTSecret = defaultJWTSecret }, want: "JWT_SECRET"},
		{name: "short JWT secret", mutate: func(c *Config) { c.JWTSecret = "too-short" }, want: "JWT_SECRET"},
		{name: "long access TTL", mutate: func(c *Config) { c.JWTAccessTTL = 2 * time.Hour }, want: "JWT_ACCESS_TTL"},
		{name: "refresh not greater", mutate: func(c *Config) { c.JWTRefreshTTL = c.JWTAccessTTL }, want: "JWT_REFRESH_TTL"},
		{name: "long refresh TTL", mutate: func(c *Config) { c.JWTRefreshTTL = 31 * 24 * time.Hour }, want: "JWT_REFRESH_TTL"},
		{name: "seed enabled", mutate: func(c *Config) { c.SeedData = true }, want: "SEED_DATA"},
		{name: "auto migrate enabled", mutate: func(c *Config) { c.AutoMigrate = true }, want: "AUTO_MIGRATE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validProductionConfig()
			tt.mutate(candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() = %v, want %s error", err, tt.want)
			}
		})
	}
}

func TestConfigValidate_ProductionCORSOrigins(t *testing.T) {
	tests := []struct {
		name   string
		origin string
	}{
		{name: "wildcard", origin: "*"},
		{name: "HTTP", origin: "http://app.example.com"},
		{name: "localhost", origin: "https://localhost:3000"},
		{name: "private IPv4", origin: "https://192.168.1.10"},
		{name: "development suffix", origin: "https://app.test"},
		{name: "credentials", origin: "https://user:pass@app.example.com"},
		{name: "path", origin: "https://app.example.com/login"},
		{name: "query", origin: "https://app.example.com?preview=1"},
		{name: "fragment", origin: "https://app.example.com#x"},
		{name: "empty member", origin: "https://app.example.com,,https://preview.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validProductionConfig()
			candidate.CORSOrigins = tt.origin
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), "CORS_ORIGINS") {
				t.Fatalf("Validate(%q) = %v, want CORS error", tt.origin, err)
			}
		})
	}

	candidate := validProductionConfig()
	candidate.CORSOrigins = " https://app.example.com , https://preview.example.com:443 "
	if err := candidate.Validate(); err != nil {
		t.Fatalf("trimmed exact origins should validate: %v", err)
	}
	if candidate.CORSOrigins != "https://app.example.com,https://preview.example.com:443" {
		t.Fatalf("normalized origins = %q", candidate.CORSOrigins)
	}
}

func TestLoad_ParsesSplitMinioEndpoints(t *testing.T) {
	t.Setenv("MINIO_ENDPOINT", "legacy-minio:9000")
	t.Setenv("MINIO_USE_SSL", "true")
	t.Setenv("MINIO_PUBLIC_URL", "https://legacy-storage.example")
	t.Setenv("MINIO_INTERNAL_ENDPOINT", "scc-minio:9000")
	t.Setenv("MINIO_PUBLIC_ENDPOINT", "storage.example")
	t.Setenv("MINIO_INTERNAL_USE_SSL", "false")
	t.Setenv("MINIO_PUBLIC_USE_SSL", "true")

	cfg := Load()

	if cfg.MinioInternalEndpoint != "scc-minio:9000" || cfg.MinioInternalUseSSL {
		t.Fatalf("unexpected internal endpoint: %q secure=%t", cfg.MinioInternalEndpoint, cfg.MinioInternalUseSSL)
	}
	if cfg.MinioPublicEndpoint != "storage.example" || !cfg.MinioPublicUseSSL {
		t.Fatalf("unexpected public endpoint: %q secure=%t", cfg.MinioPublicEndpoint, cfg.MinioPublicUseSSL)
	}
}

func TestLoad_LegacyMinioVariablesRemainSupportedInDevelopment(t *testing.T) {
	t.Setenv("ENV", "development")
	t.Setenv("MINIO_ENDPOINT", "minio.internal:9000")
	t.Setenv("MINIO_USE_SSL", "false")
	t.Setenv("MINIO_PUBLIC_URL", "https://storage.example")
	t.Setenv("MINIO_INTERNAL_ENDPOINT", "")
	t.Setenv("MINIO_PUBLIC_ENDPOINT", "")
	t.Setenv("MINIO_INTERNAL_USE_SSL", "")
	t.Setenv("MINIO_PUBLIC_USE_SSL", "")

	cfg := Load()

	if cfg.MinioInternalEndpoint != "minio.internal:9000" || cfg.MinioPublicEndpoint != "storage.example" {
		t.Fatalf("unexpected legacy mapping: internal=%q public=%q", cfg.MinioInternalEndpoint, cfg.MinioPublicEndpoint)
	}
	if cfg.MinioInternalUseSSL || !cfg.MinioPublicUseSSL {
		t.Fatalf("unexpected legacy TLS mapping: internal=%t public=%t", cfg.MinioInternalUseSSL, cfg.MinioPublicUseSSL)
	}
}

func TestConfigValidate_ProductionMinioTopology(t *testing.T) {
	valid := validProductionConfig()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid config: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "same endpoint", mutate: func(c *Config) { c.MinioInternalEndpoint = c.MinioPublicEndpoint }, want: "must be distinct"},
		{name: "public URL instead of endpoint", mutate: func(c *Config) { c.MinioPublicEndpoint = "https://storage.example" }, want: "MINIO_PUBLIC_ENDPOINT"},
		{name: "internal TLS", mutate: func(c *Config) { c.MinioInternalUseSSL = true }, want: "MINIO_INTERNAL_USE_SSL"},
		{name: "insecure public", mutate: func(c *Config) { c.MinioPublicUseSSL = false }, want: "MINIO_PUBLIC_USE_SSL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := *valid
			test.mutate(&candidate)
			err := candidate.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestConfigValidate_RejectsInvalidProductionTLSBoolean(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("MINIO_INTERNAL_ENDPOINT", "scc-minio:9000")
	t.Setenv("MINIO_PUBLIC_ENDPOINT", "storage.example")
	t.Setenv("MINIO_INTERNAL_USE_SSL", "definitely-not-a-bool")
	t.Setenv("MINIO_PUBLIC_USE_SSL", "true")

	err := Load().Validate()
	if err == nil || !strings.Contains(err.Error(), "MINIO_INTERNAL_USE_SSL must be exactly true or false") {
		t.Fatalf("Validate() = %v", err)
	}
}
