package user

import "time"

// User represents a system user.
type User struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	OfficeID     *string   `json:"officeId,omitempty"`
	IsActive     bool      `json:"isActive"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// WorkHub represents a regional hub that contains multiple offices.
type WorkHub struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
}

// Office represents an electricity authority office under a WorkHub.
type Office struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	WorkHubID string    `json:"workHubId"`
	CreatedAt time.Time `json:"createdAt"`
}

// RefreshToken represents a stored refresh token (hashed).
type RefreshToken struct {
	ID        string     `json:"id"`
	UserID    string     `json:"userId"`
	TokenHash string     `json:"-"`
	ExpiresAt time.Time  `json:"expiresAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
}
