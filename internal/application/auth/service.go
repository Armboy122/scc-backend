package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/smartcover/backend/internal/domain/user"
	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned when login credentials are wrong.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrInvalidToken is returned when a refresh token is invalid or revoked.
var ErrInvalidToken = errors.New("invalid or expired token")

// ErrUserInactive is returned when the user account is disabled.
var ErrUserInactive = errors.New("user account is inactive")

// A fixed valid bcrypt hash makes a missing username pay the same dominant
// password-comparison cost as an existing username without creating a user.
var dummyPasswordHash = []byte("$2y$12$O/v/SPN.htlX9K7GOi5tTeigQvskYJjV.mWBxFl5CQ31wRK9d/xQu")

// Claims holds the JWT payload.
type Claims struct {
	jwt.RegisteredClaims
	Role     string  `json:"role"`
	OfficeID *string `json:"officeId,omitempty"`
}

// TokenPair holds an access and refresh token pair.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
}

// Service handles authentication operations.
type Service struct {
	userRepo        user.UserRepository
	tokenRepo       user.RefreshTokenRepository
	jwtSecret       []byte
	accessTTL       time.Duration
	refreshTTL      time.Duration
	comparePassword func(hashedPassword, password []byte) error
}

// NewService creates a new auth Service.
func NewService(
	userRepo user.UserRepository,
	tokenRepo user.RefreshTokenRepository,
	jwtSecret string,
	accessTTL, refreshTTL time.Duration,
) *Service {
	return &Service{
		userRepo:        userRepo,
		tokenRepo:       tokenRepo,
		jwtSecret:       []byte(jwtSecret),
		accessTTL:       accessTTL,
		refreshTTL:      refreshTTL,
		comparePassword: bcrypt.CompareHashAndPassword,
	}
}

// Login validates credentials and returns a token pair + user.
func (s *Service) Login(ctx context.Context, username, password string) (*TokenPair, *user.User, error) {
	normalizedUsername := strings.TrimSpace(username)
	u, err := s.userRepo.FindByUsername(ctx, normalizedUsername)
	if err != nil {
		return nil, nil, err
	}
	comparePassword := s.comparePassword
	if comparePassword == nil {
		comparePassword = bcrypt.CompareHashAndPassword
	}
	if u == nil {
		_ = comparePassword(dummyPasswordHash, []byte(password))
		return nil, nil, ErrInvalidCredentials
	}
	if err := comparePassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, nil, ErrInvalidCredentials
	}
	if !u.IsActive {
		return nil, nil, ErrUserInactive
	}

	pair, err := s.issueTokenPair(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	return pair, u, nil
}

// Refresh validates a refresh token and returns a new token pair + user (rotate pattern).
func (s *Service) Refresh(ctx context.Context, rawToken string) (*TokenPair, *user.User, error) {
	now := time.Now().UTC()
	hash := hashToken(rawToken)
	rt, err := s.tokenRepo.FindByHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	if rt == nil || rt.RevokedAt != nil || !rt.ExpiresAt.After(now) {
		return nil, nil, ErrInvalidToken
	}

	u, err := s.userRepo.FindByID(ctx, rt.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u == nil || !u.IsActive {
		return nil, nil, ErrUserInactive
	}

	pair, replacement, err := s.buildTokenPair(u, now)
	if err != nil {
		return nil, nil, err
	}
	rotated, err := s.tokenRepo.Rotate(ctx, rt.ID, replacement, now)
	if err != nil {
		return nil, nil, fmt.Errorf("rotate refresh token: %w", err)
	}
	if !rotated {
		return nil, nil, ErrInvalidToken
	}
	return pair, u, nil
}

// Logout revokes a refresh token.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	hash := hashToken(rawToken)
	rt, err := s.tokenRepo.FindByHash(ctx, hash)
	if err != nil {
		return err
	}
	if rt == nil {
		return nil // idempotent
	}
	return s.tokenRepo.Revoke(ctx, rt.ID)
}

// Me returns the user for the given ID.
func (s *Service) Me(ctx context.Context, userID string) (*user.User, error) {
	u, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	if u == nil || !u.IsActive {
		return nil, ErrUserInactive
	}
	return u, nil
}

// ParseAccessToken parses and validates a JWT access token.
func (s *Service) ParseAccessToken(tokenStr string) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.jwtSecret, nil
	},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, ErrInvalidToken
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}
	if err := validateIdentityClaims(claims); err != nil {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (s *Service) issueTokenPair(ctx context.Context, u *user.User) (*TokenPair, error) {
	now := time.Now().UTC()
	pair, refreshToken, err := s.buildTokenPair(u, now)
	if err != nil {
		return nil, err
	}
	if err := s.tokenRepo.Create(ctx, refreshToken); err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}
	return pair, nil
}

func (s *Service) buildTokenPair(u *user.User, now time.Time) (*TokenPair, *user.RefreshToken, error) {
	jti := uuid.NewString()

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			ExpiresAt: jwt.NewNumericDate(now.Add(s.accessTTL)),
			IssuedAt:  jwt.NewNumericDate(now),
			ID:        jti,
		},
		Role:     string(u.Role),
		OfficeID: u.OfficeID,
	}
	if err := validateIdentityClaims(&claims); err != nil {
		return nil, nil, fmt.Errorf("invalid access token identity claims: %w", err)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("sign access token: %w", err)
	}

	// Generate opaque refresh token
	rawRefresh := uuid.NewString() + uuid.NewString()
	hashRefresh := hashToken(rawRefresh)

	refreshToken := &user.RefreshToken{
		ID:        uuid.NewString(),
		UserID:    u.ID,
		TokenHash: hashRefresh,
		ExpiresAt: now.Add(s.refreshTTL),
		CreatedAt: now,
	}
	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	}, refreshToken, nil
}

func validateIdentityClaims(claims *Claims) error {
	if claims == nil || claims.Subject == "" || claims.Subject != strings.TrimSpace(claims.Subject) {
		return fmt.Errorf("subject is required")
	}
	role := user.Role(claims.Role)
	if !role.IsValid() {
		return fmt.Errorf("role is invalid")
	}
	if claims.OfficeID != nil {
		officeID := strings.TrimSpace(*claims.OfficeID)
		if officeID == "" || officeID != *claims.OfficeID {
			return fmt.Errorf("office claim is invalid")
		}
	}
	if role.RequiresOffice() && claims.OfficeID == nil {
		return fmt.Errorf("office claim is required for role %s", role)
	}
	return nil
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
