package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/smartcover/backend/internal/domain/user"
)

type timingUserRepo struct {
	result       *user.User
	seenUsername string
}

func (r *timingUserRepo) FindByID(context.Context, string) (*user.User, error) { return nil, nil }
func (r *timingUserRepo) FindByUsername(_ context.Context, username string) (*user.User, error) {
	r.seenUsername = username
	return r.result, nil
}
func (r *timingUserRepo) Create(context.Context, *user.User) error { return nil }
func (r *timingUserRepo) Update(context.Context, *user.User) error { return nil }
func (r *timingUserRepo) List(context.Context, user.UserFilter) ([]*user.User, int64, error) {
	return nil, 0, nil
}

func TestLoginNormalizesUsernameAndRunsDummyBcryptForMissingUser(t *testing.T) {
	repo := &timingUserRepo{}
	svc := NewService(repo, nil, "test-secret", 0, 0)
	compareCalls := 0
	svc.comparePassword = func(hash, password []byte) error {
		compareCalls++
		if string(hash) != string(dummyPasswordHash) {
			t.Fatalf("hash = %q, want dummy bcrypt hash", hash)
		}
		if string(password) != "candidate" {
			t.Fatalf("password = %q", password)
		}
		return errors.New("mismatch")
	}

	_, _, err := svc.Login(context.Background(), "  Missing-User  ", "candidate")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
	if repo.seenUsername != "Missing-User" {
		t.Fatalf("repository username = %q, want normalized", repo.seenUsername)
	}
	if compareCalls != 1 {
		t.Fatalf("dummy compare calls = %d, want 1", compareCalls)
	}
}

func TestLoginComparesPasswordBeforeRevealingInactiveState(t *testing.T) {
	repo := &timingUserRepo{result: &user.User{PasswordHash: "stored", IsActive: false}}
	svc := NewService(repo, nil, "test-secret", 0, 0)
	compared := false
	svc.comparePassword = func(hash, password []byte) error {
		compared = true
		return nil
	}

	_, _, err := svc.Login(context.Background(), "inactive", "candidate")
	if !errors.Is(err, ErrUserInactive) || !compared {
		t.Fatalf("Login() error=%v compared=%t, want inactive after compare", err, compared)
	}
}
