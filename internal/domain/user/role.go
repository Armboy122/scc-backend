package user

// Role represents the user role in the system.
type Role string

const (
	RoleAdmin Role = "admin"
	RoleExec  Role = "exec"
	RoleTech  Role = "tech"
)

// IsValid returns true if the role is a known value.
func (r Role) IsValid() bool {
	switch r {
	case RoleAdmin, RoleExec, RoleTech:
		return true
	}
	return false
}

// RequiresOffice returns true for roles that must be associated with an office.
func (r Role) RequiresOffice() bool {
	return r == RoleExec || r == RoleTech
}
