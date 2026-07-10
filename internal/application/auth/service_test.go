package auth_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/smartcover/backend/internal/application/auth"
	"github.com/smartcover/backend/internal/domain/user"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"golang.org/x/crypto/bcrypt"
)

// --- Mocks ---

type mockUserRepo struct{ mock.Mock }

func (m *mockUserRepo) FindByID(ctx context.Context, id string) (*user.User, error) {
	args := m.Called(ctx, id)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *mockUserRepo) FindByUsername(ctx context.Context, username string) (*user.User, error) {
	args := m.Called(ctx, username)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.User), args.Error(1)
}

func (m *mockUserRepo) Create(ctx context.Context, u *user.User) error {
	return m.Called(ctx, u).Error(0)
}

func (m *mockUserRepo) Update(ctx context.Context, u *user.User) error {
	return m.Called(ctx, u).Error(0)
}

func (m *mockUserRepo) List(ctx context.Context, filter user.UserFilter) ([]*user.User, int64, error) {
	args := m.Called(ctx, filter)
	return args.Get(0).([]*user.User), args.Get(1).(int64), args.Error(2)
}

type mockTokenRepo struct{ mock.Mock }

func (m *mockTokenRepo) Create(ctx context.Context, rt *user.RefreshToken) error {
	return m.Called(ctx, rt).Error(0)
}

func (m *mockTokenRepo) FindByHash(ctx context.Context, hash string) (*user.RefreshToken, error) {
	args := m.Called(ctx, hash)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*user.RefreshToken), args.Error(1)
}

func (m *mockTokenRepo) Rotate(
	ctx context.Context,
	currentID string,
	replacement *user.RefreshToken,
	now time.Time,
) (bool, error) {
	args := m.Called(ctx, currentID, replacement, now)
	return args.Bool(0), args.Error(1)
}

func (m *mockTokenRepo) Revoke(ctx context.Context, id string) error {
	return m.Called(ctx, id).Error(0)
}

func (m *mockTokenRepo) DeleteExpired(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

// --- Helpers ---

const testJWTSecret = "test-secret-at-least-32-chars-ok!"

func newTestService(userRepo user.UserRepository, tokenRepo user.RefreshTokenRepository) *auth.Service {
	return auth.NewService(userRepo, tokenRepo, testJWTSecret, 15*time.Minute, 720*time.Hour)
}

func hashPassword(pw string) string {
	h, _ := bcrypt.GenerateFromPassword([]byte(pw), 12)
	return string(h)
}

// tokenHash mirrors the private hashToken function in auth/service.go.
func tokenHash(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func signAccessClaims(t *testing.T, method jwt.SigningMethod, claims auth.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign access claims: %v", err)
	}
	return signed
}

// --- Tests ---

func TestLogin_Success(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	officeID := "office-1"
	testUser := &user.User{
		ID:           "user-1",
		Username:     "tech1",
		PasswordHash: hashPassword("password123"),
		Role:         user.RoleTech,
		OfficeID:     &officeID,
		IsActive:     true,
	}

	userRepo.On("FindByUsername", mock.Anything, "tech1").Return(testUser, nil)
	tokenRepo.On("Create", mock.Anything, mock.AnythingOfType("*user.RefreshToken")).Return(nil)

	pair, u, err := svc.Login(context.Background(), "tech1", "password123")

	assert.NoError(t, err)
	assert.NotNil(t, pair)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	assert.Equal(t, testUser.ID, u.ID)
	userRepo.AssertExpectations(t)
	tokenRepo.AssertExpectations(t)
}

func TestLogin_WrongPassword(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	testUser := &user.User{
		ID:           "user-1",
		Username:     "tech1",
		PasswordHash: hashPassword("correct-password"),
		IsActive:     true,
	}
	userRepo.On("FindByUsername", mock.Anything, "tech1").Return(testUser, nil)

	_, _, err := svc.Login(context.Background(), "tech1", "wrong-password")

	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestLogin_UserNotFound(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	userRepo.On("FindByUsername", mock.Anything, "ghost").Return((*user.User)(nil), nil)

	_, _, err := svc.Login(context.Background(), "ghost", "any")

	assert.ErrorIs(t, err, auth.ErrInvalidCredentials)
}

func TestLogin_InactiveUser(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	testUser := &user.User{
		ID:           "user-1",
		Username:     "tech1",
		PasswordHash: hashPassword("password123"),
		IsActive:     false,
	}
	userRepo.On("FindByUsername", mock.Anything, "tech1").Return(testUser, nil)

	_, _, err := svc.Login(context.Background(), "tech1", "password123")

	assert.ErrorIs(t, err, auth.ErrUserInactive)
}

func TestRefresh_Success(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	officeID := "office-1"
	testUser := &user.User{
		ID: "user-1", Role: user.RoleTech, OfficeID: &officeID, IsActive: true,
	}

	rawToken := "raw-refresh-token-value-unique-123"
	storedToken := &user.RefreshToken{
		ID:        "token-1",
		UserID:    "user-1",
		TokenHash: tokenHash(rawToken),
		ExpiresAt: time.Now().Add(time.Hour),
	}

	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return(storedToken, nil)
	userRepo.On("FindByID", mock.Anything, "user-1").Return(testUser, nil)
	tokenRepo.On(
		"Rotate",
		mock.Anything,
		"token-1",
		mock.AnythingOfType("*user.RefreshToken"),
		mock.AnythingOfType("time.Time"),
	).Return(true, nil)

	pair, refreshedUser, err := svc.Refresh(context.Background(), rawToken)

	assert.NoError(t, err)
	assert.NotNil(t, pair)
	assert.Equal(t, testUser, refreshedUser)
	assert.NotEmpty(t, pair.AccessToken)
	assert.NotEmpty(t, pair.RefreshToken)
	tokenRepo.AssertCalled(t, "Rotate", mock.Anything, "token-1", mock.AnythingOfType("*user.RefreshToken"), mock.AnythingOfType("time.Time"))
}

func TestRefresh_ConditionalRotationLost_ReturnsInvalidToken(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)
	officeID := "office-1"
	testUser := &user.User{ID: "user-1", Role: user.RoleTech, OfficeID: &officeID, IsActive: true}
	rawToken := "refresh-race-loser"
	storedToken := &user.RefreshToken{
		ID: "token-1", UserID: testUser.ID, TokenHash: tokenHash(rawToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return(storedToken, nil)
	userRepo.On("FindByID", mock.Anything, testUser.ID).Return(testUser, nil)
	tokenRepo.On(
		"Rotate", mock.Anything, storedToken.ID,
		mock.AnythingOfType("*user.RefreshToken"), mock.AnythingOfType("time.Time"),
	).Return(false, nil)

	pair, refreshedUser, err := svc.Refresh(context.Background(), rawToken)

	assert.ErrorIs(t, err, auth.ErrInvalidToken)
	assert.Nil(t, pair)
	assert.Nil(t, refreshedUser)
}

func TestRefresh_RotationFailureIsPropagated(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)
	officeID := "office-1"
	testUser := &user.User{ID: "user-1", Role: user.RoleTech, OfficeID: &officeID, IsActive: true}
	rawToken := "refresh-storage-failure"
	storedToken := &user.RefreshToken{
		ID: "token-1", UserID: testUser.ID, TokenHash: tokenHash(rawToken), ExpiresAt: time.Now().Add(time.Hour),
	}
	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return(storedToken, nil)
	userRepo.On("FindByID", mock.Anything, testUser.ID).Return(testUser, nil)
	tokenRepo.On(
		"Rotate", mock.Anything, storedToken.ID,
		mock.AnythingOfType("*user.RefreshToken"), mock.AnythingOfType("time.Time"),
	).Return(false, errors.New("database unavailable"))

	_, _, err := svc.Refresh(context.Background(), rawToken)

	assert.ErrorContains(t, err, "rotate refresh token")
	assert.NotErrorIs(t, err, auth.ErrInvalidToken)
}

func TestRefresh_RevokedToken_ReturnsError(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	rawToken := "revoked-token-value"
	revokedAt := time.Now().Add(-time.Minute)
	storedToken := &user.RefreshToken{
		ID:        "token-1",
		UserID:    "user-1",
		TokenHash: tokenHash(rawToken),
		ExpiresAt: time.Now().Add(time.Hour),
		RevokedAt: &revokedAt,
	}
	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return(storedToken, nil)

	_, _, err := svc.Refresh(context.Background(), rawToken)

	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestRefresh_ExpiredToken_ReturnsError(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	rawToken := "expired-token-value"
	storedToken := &user.RefreshToken{
		ID:        "token-1",
		UserID:    "user-1",
		TokenHash: tokenHash(rawToken),
		ExpiresAt: time.Now().Add(-time.Hour), // already expired
	}
	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return(storedToken, nil)

	_, _, err := svc.Refresh(context.Background(), rawToken)

	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestRefresh_TokenNotFound_ReturnsError(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	rawToken := "nonexistent-token"
	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return((*user.RefreshToken)(nil), nil)

	_, _, err := svc.Refresh(context.Background(), rawToken)

	assert.ErrorIs(t, err, auth.ErrInvalidToken)
}

func TestLogout_RevokesToken(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	rawToken := "logout-token-value"
	storedToken := &user.RefreshToken{ID: "token-1", UserID: "user-1", TokenHash: tokenHash(rawToken)}

	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return(storedToken, nil)
	tokenRepo.On("Revoke", mock.Anything, "token-1").Return(nil)

	err := svc.Logout(context.Background(), rawToken)

	assert.NoError(t, err)
	tokenRepo.AssertCalled(t, "Revoke", mock.Anything, "token-1")
}

func TestLogout_UnknownToken_IsIdempotent(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	rawToken := "unknown-token"
	tokenRepo.On("FindByHash", mock.Anything, tokenHash(rawToken)).Return((*user.RefreshToken)(nil), nil)

	err := svc.Logout(context.Background(), rawToken)

	assert.NoError(t, err)
	tokenRepo.AssertNotCalled(t, "Revoke")
}

func TestMe_ActiveUser_ReturnsUser(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	testUser := &user.User{ID: "user-1", IsActive: true}
	userRepo.On("FindByID", mock.Anything, "user-1").Return(testUser, nil)

	u, err := svc.Me(context.Background(), "user-1")

	assert.NoError(t, err)
	assert.Equal(t, "user-1", u.ID)
}

func TestMe_InactiveUser_ReturnsError(t *testing.T) {
	userRepo := &mockUserRepo{}
	tokenRepo := &mockTokenRepo{}
	svc := newTestService(userRepo, tokenRepo)

	testUser := &user.User{ID: "user-1", IsActive: false}
	userRepo.On("FindByID", mock.Anything, "user-1").Return(testUser, nil)

	_, err := svc.Me(context.Background(), "user-1")

	assert.ErrorIs(t, err, auth.ErrUserInactive)
}

func TestParseAccessToken_RequiresHS256AndCompleteIdentityClaims(t *testing.T) {
	svc := newTestService(&mockUserRepo{}, &mockTokenRepo{})
	validClaims := func() auth.Claims {
		officeID := "office-1"
		return auth.Claims{
			RegisteredClaims: jwt.RegisteredClaims{
				Subject:   "user-1",
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
				IssuedAt:  jwt.NewNumericDate(time.Now()),
			},
			Role:     string(user.RoleTech),
			OfficeID: &officeID,
		}
	}
	tests := []struct {
		name    string
		method  jwt.SigningMethod
		mutate  func(*auth.Claims)
		wantErr bool
	}{
		{name: "valid technician", method: jwt.SigningMethodHS256},
		{
			name: "valid admin without office", method: jwt.SigningMethodHS256,
			mutate: func(claims *auth.Claims) {
				claims.Role = string(user.RoleAdmin)
				claims.OfficeID = nil
			},
		},
		{name: "HS384 rejected", method: jwt.SigningMethodHS384, wantErr: true},
		{
			name: "missing expiration rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) { claims.ExpiresAt = nil },
		},
		{
			name: "expired rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) {
				claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute))
			},
		},
		{
			name: "missing subject rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) { claims.Subject = "" },
		},
		{
			name: "padded subject rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) { claims.Subject = " user-1 " },
		},
		{
			name: "invalid role rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) { claims.Role = "manager" },
		},
		{
			name: "technician without office rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) { claims.OfficeID = nil },
		},
		{
			name: "blank office rejected", method: jwt.SigningMethodHS256, wantErr: true,
			mutate: func(claims *auth.Claims) {
				blank := "   "
				claims.OfficeID = &blank
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := validClaims()
			if tt.mutate != nil {
				tt.mutate(&claims)
			}
			raw := signAccessClaims(t, tt.method, claims)
			parsed, err := svc.ParseAccessToken(raw)
			if tt.wantErr {
				assert.ErrorIs(t, err, auth.ErrInvalidToken)
				assert.Nil(t, parsed)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, claims.Subject, parsed.Subject)
			assert.Equal(t, claims.Role, parsed.Role)
		})
	}
}
