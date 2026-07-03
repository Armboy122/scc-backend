package handler

import (
	"testing"

	"github.com/smartcover/backend/internal/domain/user"
)

func TestDefaultCreateAssignee(t *testing.T) {
	t.Run("tech-created work order defaults to self-assigned", func(t *testing.T) {
		assignee := defaultCreateAssignee(user.RoleTech, "tech-1", nil)
		if assignee == nil || *assignee != "tech-1" {
			t.Fatalf("expected tech-1 assignee, got %#v", assignee)
		}
	})

	t.Run("explicit assignee is preserved", func(t *testing.T) {
		requested := "other-tech"
		assignee := defaultCreateAssignee(user.RoleTech, "tech-1", &requested)
		if assignee == nil || *assignee != requested {
			t.Fatalf("expected explicit assignee, got %#v", assignee)
		}
	})

	t.Run("exec-created work order is unassigned by default", func(t *testing.T) {
		assignee := defaultCreateAssignee(user.RoleExec, "exec-1", nil)
		if assignee != nil {
			t.Fatalf("expected nil assignee, got %#v", assignee)
		}
	})
}
