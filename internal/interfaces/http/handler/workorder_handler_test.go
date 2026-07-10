package handler

import (
	"encoding/json"
	"testing"

	"github.com/smartcover/backend/internal/domain/user"
	woDomain "github.com/smartcover/backend/internal/domain/workorder"
)

func TestResolveCreateAssignee(t *testing.T) {
	t.Run("tech-created work order defaults to self-assigned", func(t *testing.T) {
		assignee, err := resolveCreateAssignee(user.RoleTech, "tech-1", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if assignee == nil || *assignee != "tech-1" {
			t.Fatalf("expected tech-1 assignee, got %#v", assignee)
		}
	})

	t.Run("tech cannot choose another assignee", func(t *testing.T) {
		requested := "other-tech"
		if _, err := resolveCreateAssignee(user.RoleTech, "tech-1", &requested); err == nil {
			t.Fatal("expected cross-assignee request to be rejected")
		}
	})

	t.Run("exec-created work order is unassigned by default", func(t *testing.T) {
		assignee, err := resolveCreateAssignee(user.RoleExec, "exec-1", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if assignee != nil {
			t.Fatalf("expected nil assignee, got %#v", assignee)
		}
	})
}

func TestCanCancelAndManageWorkOrderRole(t *testing.T) {
	for _, tt := range []struct {
		role    user.Role
		allowed bool
	}{
		{role: user.RoleAdmin, allowed: true},
		{role: user.RoleExec, allowed: true},
		{role: user.RoleTech},
		{role: ""},
	} {
		t.Run(string(tt.role), func(t *testing.T) {
			if got := canCancelWorkOrderRole(tt.role); got != tt.allowed {
				t.Fatalf("canCancelWorkOrderRole(%q) = %t, want %t", tt.role, got, tt.allowed)
			}
			if got := canManageWorkOrderRole(tt.role); got != tt.allowed {
				t.Fatalf("canManageWorkOrderRole(%q) = %t, want %t", tt.role, got, tt.allowed)
			}
		})
	}
}

func TestResolveWorkOrderOffice(t *testing.T) {
	officeOne := "office-1"
	for _, tt := range []struct {
		name      string
		role      user.Role
		claimed   *string
		requested string
		want      string
		wantErr   bool
	}{
		{name: "admin target", role: user.RoleAdmin, requested: "office-2", want: "office-2"},
		{name: "tech own office", role: user.RoleTech, claimed: &officeOne, requested: officeOne, want: officeOne},
		{name: "exec cross office", role: user.RoleExec, claimed: &officeOne, requested: "office-2", wantErr: true},
		{name: "non-admin missing office", role: user.RoleTech, requested: "office-1", wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveWorkOrderOffice(tt.role, tt.claimed, tt.requested)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got (%q, %v), want (%q, nil)", got, err, tt.want)
			}
		})
	}
}

func TestFieldMutationAllowed(t *testing.T) {
	officeOne := "office-1"
	officeTwo := "office-2"
	techOne := "tech-1"
	base := &woDomain.WorkOrder{OfficeID: officeOne, AssignedToID: &techOne}
	tests := []struct {
		name     string
		role     user.Role
		userID   string
		officeID *string
		wo       *woDomain.WorkOrder
		allowed  bool
	}{
		{name: "admin support", role: user.RoleAdmin, allowed: true},
		{name: "assigned same-office tech", role: user.RoleTech, userID: techOne, officeID: &officeOne, wo: base, allowed: true},
		{name: "unassigned tech", role: user.RoleTech, userID: "tech-2", officeID: &officeOne, wo: base},
		{name: "wrong-office tech", role: user.RoleTech, userID: techOne, officeID: &officeTwo, wo: base},
		{name: "exec", role: user.RoleExec, userID: "exec-1", officeID: &officeOne, wo: base},
		{name: "tech without office", role: user.RoleTech, userID: techOne, wo: base},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fieldMutationAllowed(tt.role, tt.userID, tt.officeID, tt.wo); got != tt.allowed {
				t.Fatalf("fieldMutationAllowed() = %t, want %t", got, tt.allowed)
			}
		})
	}
}

func TestJSONFieldTracksOmittedAndExplicitNull(t *testing.T) {
	var req struct {
		Phone jsonField[string] `json:"phone"`
		Note  jsonField[string] `json:"note"`
	}
	if err := json.Unmarshal([]byte(`{"phone":null}`), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !req.Phone.Present || !req.Phone.Null {
		t.Fatalf("expected phone to be present null: %#v", req.Phone)
	}
	if req.Note.Present {
		t.Fatalf("expected omitted note to remain absent: %#v", req.Note)
	}
}

func TestParseRequiredWorkOrderDate(t *testing.T) {
	valid := "2026-07-10T00:00:00+07:00"
	invalid := "2026-07-10"
	empty := ""
	tests := []struct {
		name    string
		field   string
		raw     *string
		wantErr bool
	}{
		{name: "valid RFC3339", field: "installDate", raw: &valid},
		{name: "missing", field: "installDate", raw: nil, wantErr: true},
		{name: "empty", field: "removalDate", raw: &empty, wantErr: true},
		{name: "date only is rejected", field: "removalDate", raw: &invalid, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRequiredWorkOrderDate(tt.field, tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got date %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got == nil {
				t.Fatal("expected a parsed date")
			}
		})
	}
}
