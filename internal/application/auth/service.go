package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
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
	userRepo   user.UserRepository
	tokenRepo  user.RefreshTokenRepository
	jwtSecret  []byte
	accessTTL  time.Duration
	refreshTTL time.Duration
}

// NewService creates a new auth Service.
func NewService(
	userRepo user.UserRepository,
	tokenRepo user.RefreshTokenRepository,
	jwtSecret string,
	accessTTL, refreshTTL time.Duration,
) *Service {
	return &Service{
		userRepo:   userRepo,
		tokenRepo:  tokenRepo,
		jwtSecret:  []byte(jwtSecret),
		accessTTL:  accessTTL,
		refreshTTL: refreshTTL,
	}
}

// Login validates credentials and returns a token pair + user.
func (s *Service) Login(ctx context.Context, username, password string) (*TokenPair, *user.User, error) {
	u, err := s.userRepo.FindByUsername(ctx, username)
	if err != nil {
		return nil, nil, err
	}
	if u == nil {
		return nil, nil, ErrInvalidCredentials
	}
	if !u.IsActive {
		return nil, nil, ErrUserInactive
	}

	if err := bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)); err != nil {
		return nil, nil, ErrInvalidCredentials
	}

	pair, err := s.issueTokenPair(ctx, u)
	if err != nil {
		return nil, nil, err
	}
	return pair, u, nil
}

// Refresh validates a refresh token and returns a new token pair + user (rotate pattern).
func (s *Service) Refresh(ctx context.Context, rawToken string) (*TokenPair, *user.User, error) {
	hash := hashToken(rawToken)
	rt, err := s.tokenRepo.FindByHash(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	if rt == nil || rt.RevokedAt != nil || rt.ExpiresAt.Before(time.Now()) {
		return nil, nil, ErrInvalidToken
	}

	u, err := s.userRepo.FindByID(ctx, rt.UserID)
	if err != nil {
		return nil, nil, err
	}
	if u == nil || !u.IsActive {
		return nil, nil, ErrUserInactive
	}

	// Revoke old token
	if err := s.tokenRepo.Revoke(ctx, rt.ID); err != nil {
		return nil, nil, err
	}

	pair, err := s.issueTokenPair(ctx, u)
	if err != nil {
		return nil, nil, err
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
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, ErrInvalidToken
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, ErrInvalidToken
	}
	return claims, nil
}

func (s *Service) issueTokenPair(ctx context.Context, u *user.User) (*TokenPair, error) {
	now := time.Now()
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

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	accessToken, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return nil, fmt.Errorf("sign access token: %w", err)
	}

	// Generate opaque refresh token
	rawRefresh := uuid.NewString() + uuid.NewString()
	hashRefresh := hashToken(rawRefresh)

	rt := &user.RefreshToken{
		ID:        uuid.NewString(),
		UserID:    u.ID,
		TokenHash: hashRefresh,
		ExpiresAt: now.Add(s.refreshTTL),
		CreatedAt: now,
	}
	if err := s.tokenRepo.Create(ctx, rt); err != nil {
		return nil, fmt.Errorf("store refresh token: %w", err)
	}

	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: rawRefresh,
	}, nil
}

func hashToken(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}
