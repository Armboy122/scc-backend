package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/smartcover/backend/internal/interfaces/http/middleware"
)

type stubAuthenticationService struct {
	loginCalls     int
	usernames      []string
	login          func(username, password string) (*auth.TokenPair, *user.User, error)
	refresh        func(refreshToken string) (*auth.TokenPair, *user.User, error)
	logout         func(refreshToken string) error
	me             func(userID string) (*user.User, error)
	changePassword func(userID, currentPassword, newPassword string) error
}

func (s *stubAuthenticationService) Login(_ context.Context, username, password string) (*auth.TokenPair, *user.User, error) {
	s.loginCalls++
	s.usernames = append(s.usernames, username)
	return s.login(username, password)
}
func (s *stubAuthenticationService) Refresh(_ context.Context, refreshToken string) (*auth.TokenPair, *user.User, error) {
	if s.refresh == nil {
		return nil, nil, errors.New("unused")
	}
	return s.refresh(refreshToken)
}
func (s *stubAuthenticationService) Logout(_ context.Context, refreshToken string) error {
	if s.logout == nil {
		return nil
	}
	return s.logout(refreshToken)
}
func (s *stubAuthenticationService) Me(_ context.Context, userID string) (*user.User, error) {
	if s.me == nil {
		return nil, errors.New("unused")
	}
	return s.me(userID)
}
func (s *stubAuthenticationService) ChangePassword(_ context.Context, userID, currentPassword, newPassword string) error {
	if s.changePassword == nil {
		return errors.New("unused")
	}
	return s.changePassword(userID, currentPassword, newPassword)
}

func testLoginLimiterConfig() loginLimiterConfig {
	now := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	return loginLimiterConfig{
		ClientLimit: 100, ClientWindow: 5 * time.Minute,
		IdentityLimit: 100, IdentityWindow: 15 * time.Minute,
		MaxEntriesPerMap: 32, CleanupInterval: time.Minute,
		Now: func() time.Time { return now },
	}
}

func performLogin(h *AuthHandler, username, password, forwardedFor string) *httptest.ResponseRecorder {
	body := `{"username":"` + username + `","password":"` + password + `"}`
	request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	request.RemoteAddr = "192.0.2.10:4321"
	if forwardedFor != "" {
		request.Header.Set("X-Forwarded-For", forwardedFor)
	}
	recorder := httptest.NewRecorder()
	h.Login(recorder, request)
	return recorder
}

func TestLoginLimiterBlocksTargetedUsernameGuessingWithRetryAfter(t *testing.T) {
	service := &stubAuthenticationService{login: func(string, string) (*auth.TokenPair, *user.User, error) {
		return nil, nil, auth.ErrInvalidCredentials
	}}
	config := testLoginLimiterConfig()
	config.IdentityLimit = 2
	h := &AuthHandler{svc: service, loginLimiter: newLoginAttemptLimiter(config)}

	for i := 0; i < 2; i++ {
		if response := performLogin(h, "  Admin  ", "wrong", "203.0.113.9"); response.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d status = %d", i+1, response.Code)
		}
	}
	response := performLogin(h, "admin", "wrong", "203.0.113.9")
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d, want 429; body=%s", response.Code, response.Body.String())
	}
	retryAfter, err := strconv.Atoi(response.Header().Get("Retry-After"))
	if err != nil || retryAfter < 1 {
		t.Fatalf("Retry-After = %q", response.Header().Get("Retry-After"))
	}
	if service.loginCalls != 2 {
		t.Fatalf("service login calls = %d, want 2", service.loginCalls)
	}
	for _, username := range service.usernames {
		if username != "Admin" {
			t.Fatalf("service received unnormalized username %q", username)
		}
	}
}

func TestLoginLimiterAlsoCapsOneClientAcrossUsernames(t *testing.T) {
	service := &stubAuthenticationService{login: func(string, string) (*auth.TokenPair, *user.User, error) {
		return nil, nil, auth.ErrInvalidCredentials
	}}
	config := testLoginLimiterConfig()
	config.ClientLimit = 2
	h := &AuthHandler{svc: service, loginLimiter: newLoginAttemptLimiter(config)}

	for _, username := range []string{"one", "two"} {
		if response := performLogin(h, username, "wrong", "203.0.113.10"); response.Code != http.StatusUnauthorized {
			t.Fatalf("username %s status = %d", username, response.Code)
		}
	}
	if response := performLogin(h, "three", "wrong", "203.0.113.10"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("cross-username status = %d, want 429", response.Code)
	}
}

func TestSuccessfulLoginResetsOnlyMatchingIdentityWindow(t *testing.T) {
	service := &stubAuthenticationService{login: func(_ string, password string) (*auth.TokenPair, *user.User, error) {
		if password != "correct" {
			return nil, nil, auth.ErrInvalidCredentials
		}
		return &auth.TokenPair{AccessToken: "access", RefreshToken: "refresh"}, &user.User{
			ID: "user-1", Name: "Admin", Username: "admin", Role: user.RoleAdmin, IsActive: true,
		}, nil
	}}
	config := testLoginLimiterConfig()
	config.ClientLimit = 100
	config.IdentityLimit = 2
	limiter := newLoginAttemptLimiter(config)
	h := &AuthHandler{svc: service, loginLimiter: limiter}

	if response := performLogin(h, "admin", "wrong", "203.0.113.11"); response.Code != http.StatusUnauthorized {
		t.Fatalf("first failure status = %d", response.Code)
	}
	if response := performLogin(h, "admin", "correct", "203.0.113.11"); response.Code != http.StatusOK {
		t.Fatalf("success status = %d; body=%s", response.Code, response.Body.String())
	}
	if clients, identities := limiter.sizes(); clients != 1 || identities != 0 {
		t.Fatalf("windows after success clients=%d identities=%d, want client retained and identity reset", clients, identities)
	}
	for i := 0; i < 2; i++ {
		if response := performLogin(h, "admin", "wrong", "203.0.113.11"); response.Code != http.StatusUnauthorized {
			t.Fatalf("post-reset failure %d status = %d", i+1, response.Code)
		}
	}
}

func TestSuccessfulAccountCannotResetBroadClientBudget(t *testing.T) {
	service := &stubAuthenticationService{login: func(_ string, password string) (*auth.TokenPair, *user.User, error) {
		if password != "correct" {
			return nil, nil, auth.ErrInvalidCredentials
		}
		return &auth.TokenPair{AccessToken: "access", RefreshToken: "refresh"}, &user.User{
			ID: "known-user", Username: "known", Role: user.RoleAdmin, IsActive: true,
		}, nil
	}}
	config := testLoginLimiterConfig()
	config.ClientLimit = 2
	config.IdentityLimit = 10
	h := &AuthHandler{svc: service, loginLimiter: newLoginAttemptLimiter(config)}

	if response := performLogin(h, "target", "wrong", "203.0.113.12"); response.Code != http.StatusUnauthorized {
		t.Fatalf("guess status = %d", response.Code)
	}
	if response := performLogin(h, "known", "correct", "203.0.113.12"); response.Code != http.StatusOK {
		t.Fatalf("known-account status = %d", response.Code)
	}
	if response := performLogin(h, "another-target", "wrong", "203.0.113.12"); response.Code != http.StatusTooManyRequests {
		t.Fatalf("post-success broad limit status = %d, want 429", response.Code)
	}
}

func TestLoginClientIPUsesLastValidForwardedHopThenRemoteAddr(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.RemoteAddr = "192.0.2.22:1234"
	request.Header.Set("X-Forwarded-For", "garbage, 203.0.113.8, 198.51.100.7, invalid")
	if got := loginClientIP(request); got != "198.51.100.7" {
		t.Fatalf("forwarded client IP = %q", got)
	}

	request.Header.Del("X-Forwarded-For")
	request.RemoteAddr = "[2001:db8::7]:4321"
	if got := loginClientIP(request); got != "2001:db8::7" {
		t.Fatalf("remote client IP = %q", got)
	}
}

func TestLoginLimiterBoundsAndCleansState(t *testing.T) {
	now := time.Date(2026, 7, 10, 3, 0, 0, 0, time.UTC)
	config := testLoginLimiterConfig()
	config.MaxEntriesPerMap = 2
	config.ClientWindow = time.Minute
	config.IdentityWindow = time.Minute
	config.Now = func() time.Time { return now }
	limiter := newLoginAttemptLimiter(config)
	for _, client := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"} {
		limiter.RecordFailure(client, "admin")
	}
	clients, identities := limiter.sizes()
	if clients > 2 || identities > 2 {
		t.Fatalf("bounded map sizes clients=%d identities=%d", clients, identities)
	}

	now = now.Add(2 * time.Minute)
	_, _ = limiter.RetryAfter("192.0.2.99", "admin")
	if clients, identities = limiter.sizes(); clients != 0 || identities != 0 {
		t.Fatalf("expired map sizes clients=%d identities=%d, want zero", clients, identities)
	}
}

func TestLoginLimiterAtomicallyCapsConcurrentAttempts(t *testing.T) {
	config := testLoginLimiterConfig()
	config.ClientLimit = 10
	config.IdentityLimit = 5
	limiter := newLoginAttemptLimiter(config)
	var allowed atomic.Int64
	var wait sync.WaitGroup
	for i := 0; i < 100; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if _, limited := limiter.BeginAttempt("192.0.2.50", "admin"); !limited {
				allowed.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := allowed.Load(); got != 5 {
		t.Fatalf("concurrent allowed attempts = %d, want 5", got)
	}
}

func TestLogoutDoesNotReportSuccessWhenRevocationFails(t *testing.T) {
	service := &stubAuthenticationService{
		login: func(string, string) (*auth.TokenPair, *user.User, error) {
			return nil, nil, errors.New("unused")
		},
		logout: func(refreshToken string) error {
			if refreshToken != "refresh-token" {
				t.Fatalf("logout token = %q", refreshToken)
			}
			return errors.New("database unavailable")
		},
	}
	h := &AuthHandler{svc: service}
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/logout",
		strings.NewReader(`{"refreshToken":"refresh-token"}`),
	)
	recorder := httptest.NewRecorder()

	h.Logout(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("logout status = %d, want 500; body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestPublicAuthEndpointsBoundAndClassifyFailures(t *testing.T) {
	t.Run("oversized login rejected before password work", func(t *testing.T) {
		service := &stubAuthenticationService{
			login: func(string, string) (*auth.TokenPair, *user.User, error) {
				t.Fatal("oversized request reached authentication service")
				return nil, nil, nil
			},
		}
		h := &AuthHandler{svc: service, loginLimiter: newLoginAttemptLimiter(testLoginLimiterConfig())}
		body := `{"username":"` + strings.Repeat("a", int(maxCanonicalRequestBytes)) + `","password":"x"}`
		request := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
		recorder := httptest.NewRecorder()

		h.Login(recorder, request)

		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("oversized login status = %d, want 400", recorder.Code)
		}
	})

	t.Run("refresh storage failure is not reported as bad credentials", func(t *testing.T) {
		service := &stubAuthenticationService{
			login: func(string, string) (*auth.TokenPair, *user.User, error) {
				return nil, nil, errors.New("unused")
			},
			refresh: func(refreshToken string) (*auth.TokenPair, *user.User, error) {
				if refreshToken != "refresh-token" {
					t.Fatalf("refresh token = %q", refreshToken)
				}
				return nil, nil, errors.New("database unavailable")
			},
		}
		h := &AuthHandler{svc: service}
		request := httptest.NewRequest(
			http.MethodPost,
			"/api/v1/auth/refresh",
			strings.NewReader(`{"refreshToken":"refresh-token"}`),
		)
		recorder := httptest.NewRecorder()

		h.Refresh(recorder, request)

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("refresh status = %d, want 500; body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("profile storage failure is not reported as an expired session", func(t *testing.T) {
		service := &stubAuthenticationService{
			login: func(string, string) (*auth.TokenPair, *user.User, error) {
				return nil, nil, errors.New("unused")
			},
			me: func(userID string) (*user.User, error) {
				if userID != "user-1" {
					t.Fatalf("profile user = %q", userID)
				}
				return nil, errors.New("database unavailable")
			},
		}
		h := &AuthHandler{svc: service}
		secret := "test-secret"
		request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		request.Header.Set("Authorization", "Bearer "+signTestAccessToken(t, secret, "admin", nil))
		recorder := httptest.NewRecorder()

		middleware.Authenticator(auth.NewService(nil, nil, secret, time.Minute, time.Hour))(
			http.HandlerFunc(h.Me),
		).ServeHTTP(recorder, request)

		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("profile status = %d, want 500; body=%s", recorder.Code, recorder.Body.String())
		}
	})
}
