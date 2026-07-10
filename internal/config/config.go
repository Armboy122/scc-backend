package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultJWTSecret  = "change-me-in-production"
	minJWTSecretBytes = 32
	minJWTAccessTTL   = time.Minute
	maxJWTAccessTTL   = time.Hour
	minJWTRefreshTTL  = time.Hour
	maxJWTRefreshTTL  = 30 * 24 * time.Hour
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

	MinioInternalEndpoint string
	MinioPublicEndpoint   string
	MinioInternalUseSSL   bool
	MinioPublicUseSSL     bool
	minioInternalSSLRaw   string
	minioPublicSSLRaw     string

	CORSOrigins            string
	EnablePhase2Borrowing  bool
	RunBackgroundJobs      bool
	seedDataRaw            string
	autoMigrateRaw         string
	jwtAccessTTLRaw        string
	jwtRefreshTTLRaw       string
	minioUseSSLRaw         string
	phase2BorrowingFlagRaw string
	runBackgroundJobsRaw   string
}

// Load reads environment variables and returns a Config with sane defaults.
func Load() *Config {
	env := getEnv("ENV", "development")
	legacyEndpoint := getEnv("MINIO_ENDPOINT", "scc-minio:9000")
	legacyUseSSL := getBoolEnv("MINIO_USE_SSL", false)
	publicURL := getEnv("MINIO_PUBLIC_URL", "http://localhost:9000")
	publicEndpoint, publicURLSecure := publicEndpointFromOrigin(publicURL)
	if explicit := strings.TrimSpace(os.Getenv("MINIO_PUBLIC_ENDPOINT")); explicit != "" {
		publicEndpoint = explicit
	}
	if publicEndpoint == "" {
		publicEndpoint = legacyEndpoint
	}
	publicUseSSL := publicURLSecure
	if _, present := os.LookupEnv("MINIO_PUBLIC_USE_SSL"); present {
		publicUseSSL = getBoolEnv("MINIO_PUBLIC_USE_SSL", publicURLSecure)
	} else if os.Getenv("MINIO_PUBLIC_URL") == "" {
		publicUseSSL = legacyUseSSL
	}
	internalUseSSL := legacyUseSSL
	if _, present := os.LookupEnv("MINIO_INTERNAL_USE_SSL"); present {
		internalUseSSL = getBoolEnv("MINIO_INTERNAL_USE_SSL", legacyUseSSL)
	}

	return &Config{
		DatabaseURL:    getEnv("DATABASE_URL", "postgres://smartcover:smartcover@scc-postgres:5432/smartcover?sslmode=disable"),
		Port:           getEnv("PORT", "8080"),
		Env:            env,
		SeedData:       getBoolEnv("SEED_DATA", false),
		AutoMigrate:    getBoolEnv("AUTO_MIGRATE", !isProductionEnvironment(env)),
		JWTSecret:      getEnv("JWT_SECRET", defaultJWTSecret),
		JWTAccessTTL:   getDurationEnv("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL:  getDurationEnv("JWT_REFRESH_TTL", 168*time.Hour),
		MinioEndpoint:  legacyEndpoint,
		MinioAccessKey: getEnv("MINIO_ACCESS_KEY", "minioadmin"),
		MinioSecretKey: getEnv("MINIO_SECRET_KEY", "minioadmin"),
		MinioBucket:    getEnv("MINIO_BUCKET", "scc"),
		MinioPublicURL: publicURL,
		MinioUseSSL:    legacyUseSSL,

		MinioInternalEndpoint:  getEnv("MINIO_INTERNAL_ENDPOINT", legacyEndpoint),
		MinioPublicEndpoint:    publicEndpoint,
		MinioInternalUseSSL:    internalUseSSL,
		MinioPublicUseSSL:      publicUseSSL,
		minioInternalSSLRaw:    os.Getenv("MINIO_INTERNAL_USE_SSL"),
		minioPublicSSLRaw:      os.Getenv("MINIO_PUBLIC_USE_SSL"),
		CORSOrigins:            getEnv("CORS_ORIGINS", "http://localhost:3000"),
		EnablePhase2Borrowing:  os.Getenv("ENABLE_PHASE2_BORROWING") == "true",
		RunBackgroundJobs:      getBoolEnv("RUN_BACKGROUND_JOBS", !isProductionEnvironment(env)),
		seedDataRaw:            os.Getenv("SEED_DATA"),
		autoMigrateRaw:         os.Getenv("AUTO_MIGRATE"),
		jwtAccessTTLRaw:        os.Getenv("JWT_ACCESS_TTL"),
		jwtRefreshTTLRaw:       os.Getenv("JWT_REFRESH_TTL"),
		minioUseSSLRaw:         os.Getenv("MINIO_USE_SSL"),
		phase2BorrowingFlagRaw: os.Getenv("ENABLE_PHASE2_BORROWING"),
		runBackgroundJobsRaw:   os.Getenv("RUN_BACKGROUND_JOBS"),
	}
}

// Validate rejects unsafe object-storage topology in production. Development
// retains legacy single-endpoint parsing for backwards compatibility, while a
// production API must use a private internal endpoint and a distinct HTTPS
// public signer host.
func (c *Config) Validate() error {
	for name, raw := range map[string]string{
		"SEED_DATA":               c.seedDataRaw,
		"AUTO_MIGRATE":            c.autoMigrateRaw,
		"MINIO_USE_SSL":           c.minioUseSSLRaw,
		"MINIO_INTERNAL_USE_SSL":  c.minioInternalSSLRaw,
		"MINIO_PUBLIC_USE_SSL":    c.minioPublicSSLRaw,
		"ENABLE_PHASE2_BORROWING": c.phase2BorrowingFlagRaw,
		"RUN_BACKGROUND_JOBS":     c.runBackgroundJobsRaw,
	} {
		if raw != "" && raw != "true" && raw != "false" {
			return fmt.Errorf("%s must be exactly true or false", name)
		}
	}
	for name, raw := range map[string]string{
		"JWT_ACCESS_TTL":  c.jwtAccessTTLRaw,
		"JWT_REFRESH_TTL": c.jwtRefreshTTLRaw,
	} {
		if raw != "" {
			if _, err := time.ParseDuration(raw); err != nil {
				return fmt.Errorf("%s must be a valid duration", name)
			}
		}
	}
	if !strings.EqualFold(strings.TrimSpace(c.Env), "production") {
		return nil
	}
	if c.runBackgroundJobsRaw == "" {
		return errors.New("RUN_BACKGROUND_JOBS must be explicitly true or false in production")
	}
	if c.SeedData {
		return errors.New("SEED_DATA must be false in production")
	}
	if c.AutoMigrate {
		return errors.New("AUTO_MIGRATE must be false in production")
	}
	secret := strings.TrimSpace(c.JWTSecret)
	if secret == defaultJWTSecret || len([]byte(secret)) < minJWTSecretBytes {
		return fmt.Errorf("JWT_SECRET must be a non-default secret of at least %d bytes in production", minJWTSecretBytes)
	}
	if c.JWTAccessTTL < minJWTAccessTTL || c.JWTAccessTTL > maxJWTAccessTTL {
		return fmt.Errorf("JWT_ACCESS_TTL must be between %s and %s in production", minJWTAccessTTL, maxJWTAccessTTL)
	}
	if c.JWTRefreshTTL < minJWTRefreshTTL || c.JWTRefreshTTL > maxJWTRefreshTTL || c.JWTRefreshTTL <= c.JWTAccessTTL {
		return fmt.Errorf("JWT_REFRESH_TTL must be greater than JWT_ACCESS_TTL and between %s and %s in production", minJWTRefreshTTL, maxJWTRefreshTTL)
	}
	normalizedOrigins, err := validateProductionCORSOrigins(c.CORSOrigins)
	if err != nil {
		return fmt.Errorf("CORS_ORIGINS: %w", err)
	}
	c.CORSOrigins = normalizedOrigins
	if err := validateEndpoint(c.MinioInternalEndpoint); err != nil {
		return fmt.Errorf("MINIO_INTERNAL_ENDPOINT: %w", err)
	}
	if err := validateEndpoint(c.MinioPublicEndpoint); err != nil {
		return fmt.Errorf("MINIO_PUBLIC_ENDPOINT: %w", err)
	}
	if strings.EqualFold(c.MinioInternalEndpoint, c.MinioPublicEndpoint) {
		return errors.New("MINIO_INTERNAL_ENDPOINT and MINIO_PUBLIC_ENDPOINT must be distinct in production")
	}
	if c.MinioInternalUseSSL {
		return errors.New("MINIO_INTERNAL_USE_SSL must be false for the Docker-network MinIO endpoint")
	}
	if !c.MinioPublicUseSSL {
		return errors.New("MINIO_PUBLIC_USE_SSL must be true in production")
	}
	if strings.TrimSpace(c.MinioAccessKey) == "" || strings.TrimSpace(c.MinioSecretKey) == "" || strings.TrimSpace(c.MinioBucket) == "" {
		return errors.New("MINIO_ACCESS_KEY, MINIO_SECRET_KEY, and MINIO_BUCKET are required in production")
	}
	return nil
}

func isProductionEnvironment(env string) bool {
	return strings.EqualFold(strings.TrimSpace(env), "production")
}

func validateProductionCORSOrigins(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("at least one exact HTTPS origin is required")
	}
	parts := strings.Split(raw, ",")
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		origin := strings.TrimSpace(part)
		if origin == "" {
			return "", errors.New("empty origins are not allowed")
		}
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
			parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
			return "", fmt.Errorf("%q must be an exact HTTPS origin without credentials, path, query, or fragment", origin)
		}
		hostname := strings.ToLower(parsed.Hostname())
		if !isProductionCORSHost(hostname) {
			return "", fmt.Errorf("%q uses a wildcard, local, private, reserved, or development host", origin)
		}
		if port := parsed.Port(); port != "" {
			value, err := strconv.Atoi(port)
			if err != nil || value < 1 || value > 65535 {
				return "", fmt.Errorf("%q has an invalid port", origin)
			}
		}
		normalized = append(normalized, origin)
	}
	return strings.Join(normalized, ","), nil
}

func isProductionCORSHost(hostname string) bool {
	if hostname == "" || strings.Contains(hostname, "*") {
		return false
	}
	if ip := net.ParseIP(hostname); ip != nil {
		return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() &&
			!ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsUnspecified()
	}
	if !validDNSHostname(hostname) || !strings.Contains(hostname, ".") {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".lan", ".home", ".internal", ".test", ".invalid", ".example"} {
		if hostname == strings.TrimPrefix(suffix, ".") || strings.HasSuffix(hostname, suffix) {
			return false
		}
	}
	return true
}

func validDNSHostname(hostname string) bool {
	if len(hostname) > 253 || strings.HasPrefix(hostname, ".") || strings.HasSuffix(hostname, ".") {
		return false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return false
			}
		}
	}
	return true
}

func publicEndpointFromOrigin(origin string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(origin))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		(parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", false
	}
	return parsed.Host, parsed.Scheme == "https"
}

func validateEndpoint(endpoint string) error {
	if endpoint == "" || strings.TrimSpace(endpoint) != endpoint || strings.Contains(endpoint, "://") ||
		strings.ContainsAny(endpoint, "/?#@\r\n\t ") {
		return errors.New("must be a host[:port] without a scheme, credentials, path, query, or fragment")
	}
	parsed, err := url.Parse("//" + endpoint)
	if err != nil || parsed.Host != endpoint || parsed.Hostname() == "" {
		return errors.New("must be a valid host[:port]")
	}
	return nil
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
