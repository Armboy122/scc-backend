package user

import (
	"context"
	"time"
)

// UserRepository defines persistence operations for User.
type UserRepository interface {
	FindByID(ctx context.Context, id string) (*User, error)
	FindByUsername(ctx context.Context, username string) (*User, error)
	Create(ctx context.Context, u *User) error
	Update(ctx context.Context, u *User) error
	List(ctx context.Context, filter UserFilter) ([]*User, int64, error)
}

// TechnicianOption is the least-privilege projection used by assignment
// pickers. Authentication fields and usernames are deliberately absent.
type TechnicianOption struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OfficeID string `json:"officeId"`
}

// TechnicianRepository exposes only active technicians for one exact office.
type TechnicianRepository interface {
	ListActiveTechniciansByOffice(ctx context.Context, officeID string) ([]TechnicianOption, error)
}

// UserFilter holds optional filtering criteria for listing users.
type UserFilter struct {
	Query    *string
	OfficeID *string
	Role     *Role
	IsActive *bool
	Page     int
	Limit    int
}

// WorkHubRepository defines persistence operations for WorkHub.
type WorkHubRepository interface {
	FindByID(ctx context.Context, id string) (*WorkHub, error)
	List(ctx context.Context) ([]*WorkHub, error)
	Create(ctx context.Context, wh *WorkHub) error
	Update(ctx context.Context, wh *WorkHub) error
}

// OfficeRepository defines persistence operations for Office.
type OfficeRepository interface {
	FindByID(ctx context.Context, id string) (*Office, error)
	List(ctx context.Context) ([]*Office, error)
	Create(ctx context.Context, o *Office) error
	Update(ctx context.Context, o *Office) error
}

// RefreshTokenRepository defines persistence operations for RefreshToken.
type RefreshTokenRepository interface {
	Create(ctx context.Context, rt *RefreshToken) error
	FindByHash(ctx context.Context, hash string) (*RefreshToken, error)
	Rotate(ctx context.Context, currentID string, replacement *RefreshToken, now time.Time) (bool, error)
	Revoke(ctx context.Context, id string) error
	RevokeAllByUserID(ctx context.Context, userID string) error
	DeleteExpired(ctx context.Context) error
}
