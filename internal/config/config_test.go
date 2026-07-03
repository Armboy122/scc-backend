package config

import (
	"testing"
	"time"
)

func TestLoad_DefaultRefreshTTLIsSevenDays(t *testing.T) {
	t.Setenv("JWT_REFRESH_TTL", "")

	cfg := Load()

	if cfg.JWTRefreshTTL != 168*time.Hour {
		t.Fatalf("expected default refresh TTL 168h, got %s", cfg.JWTRefreshTTL)
	}
}
