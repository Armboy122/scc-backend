package user

import "context"

// UserRepository defines persistence operations for User.
type UserRepository interface {
	FindByID(ctx context.Context, id string) (*User, error)
	FindByUsername(ctx context.Context, username string) (*User, error)
	Create(ctx context.Context, u *User) error
	Update(ctx context.Context, u *User) error
	List(ctx context.Context, filter UserFilter) ([]*User, int64, error)
}

// UserFilter holds optional filtering criteria for listing users.
type UserFilter struct {
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
}

// OfficeRepository defines persistence operations for Office.
type OfficeRepository interface {
	FindByID(ctx context.Context, id string) (*Office, error)
	List(ctx context.Context) ([]*Office, error)
	Create(ctx context.Context, o *Office) error
}

// RefreshTokenRepository defines persistence operations for RefreshToken.
type RefreshTokenRepository interface {
	Create(ctx context.Context, rt *RefreshToken) error
	FindByHash(ctx context.Context, hash string) (*RefreshToken, error)
	Revoke(ctx context.Context, id string) error
	DeleteExpired(ctx context.Context) error
}
