package auth

import (
	"testing"
)

func TestBuiltinRoles(t *testing.T) {
	roles := BuiltinRoles()

	t.Run("returns three roles", func(t *testing.T) {
		if len(roles) != 3 {
			t.Fatalf("expected 3 built-in roles, got %d", len(roles))
		}
	})

	findRole := func(id string) *Role {
		t.Helper()
		for i := range roles {
			if roles[i].ID == id {
				return &roles[i]
			}
		}
		t.Fatalf("role %q not found", id)
		return nil
	}

	t.Run("admin has 10 permissions", func(t *testing.T) {
		admin := findRole(RoleAdminID)
		if len(admin.Permissions) != 10 {
			t.Errorf("expected admin to have 10 permissions, got %d", len(admin.Permissions))
		}
		if !admin.BuiltIn {
			t.Error("admin role should be built-in")
		}
	})

	t.Run("operator has 8 permissions", func(t *testing.T) {
		op := findRole(RoleOperatorID)
		if len(op.Permissions) != 8 {
			t.Errorf("expected operator to have 8 permissions, got %d", len(op.Permissions))
		}
		if !op.BuiltIn {
			t.Error("operator role should be built-in")
		}
	})

	t.Run("viewer has 4 permissions", func(t *testing.T) {
		viewer := findRole(RoleViewerID)
		if len(viewer.Permissions) != 4 {
			t.Errorf("expected viewer to have 4 permissions, got %d", len(viewer.Permissions))
		}
		if !viewer.BuiltIn {
			t.Error("viewer role should be built-in")
		}
	})

	t.Run("admin has all permissions", func(t *testing.T) {
		admin := findRole(RoleAdminID)
		all := AllPermissions()
		if len(admin.Permissions) != len(all) {
			t.Errorf("admin should have all %d permissions, has %d", len(all), len(admin.Permissions))
		}
		allMap := make(map[Permission]bool)
		for _, p := range all {
			allMap[p] = true
		}
		for _, p := range admin.Permissions {
			if !allMap[p] {
				t.Errorf("admin permission %q not in AllPermissions()", p)
			}
		}
	})
}

func TestResolvePermissions(t *testing.T) {
	t.Run("nil role returns nil", func(t *testing.T) {
		result := ResolvePermissions(nil, nil)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("nil role with token perms returns nil", func(t *testing.T) {
		result := ResolvePermissions(nil, []Permission{PermContainersView})
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})

	t.Run("role with nil token perms returns role perms", func(t *testing.T) {
		role := &Role{
			ID:          "test",
			Permissions: []Permission{PermContainersView, PermLogsView},
		}
		result := ResolvePermissions(role, nil)
		if len(result) != 2 {
			t.Fatalf("expected 2 permissions, got %d", len(result))
		}
		if result[0] != PermContainersView || result[1] != PermLogsView {
			t.Errorf("unexpected permissions: %v", result)
		}
	})

	t.Run("intersects role and token perms", func(t *testing.T) {
		role := &Role{
			ID:          "test",
			Permissions: []Permission{PermContainersView, PermContainersUpdate, PermLogsView},
		}
		tokenPerms := []Permission{PermContainersView, PermLogsView, PermSettingsView}

		result := ResolvePermissions(role, tokenPerms)
		if len(result) != 2 {
			t.Fatalf("expected 2 permissions (intersection), got %d: %v", len(result), result)
		}
		// Result should contain only the permissions in both sets.
		resultMap := make(map[Permission]bool)
		for _, p := range result {
			resultMap[p] = true
		}
		if !resultMap[PermContainersView] {
			t.Error("expected PermContainersView in intersection")
		}
		if !resultMap[PermLogsView] {
			t.Error("expected PermLogsView in intersection")
		}
		if resultMap[PermSettingsView] {
			t.Error("PermSettingsView should NOT be in intersection (not in role)")
		}
		if resultMap[PermContainersUpdate] {
			t.Error("PermContainersUpdate should NOT be in intersection (not in token)")
		}
	})

	t.Run("empty token perms returns empty result", func(t *testing.T) {
		role := &Role{
			ID:          "test",
			Permissions: []Permission{PermContainersView},
		}
		result := ResolvePermissions(role, []Permission{})
		if len(result) != 0 {
			t.Errorf("expected empty result for empty token perms, got %v", result)
		}
	})

	t.Run("no overlap returns empty result", func(t *testing.T) {
		role := &Role{
			ID:          "test",
			Permissions: []Permission{PermContainersView},
		}
		tokenPerms := []Permission{PermSettingsModify}
		result := ResolvePermissions(role, tokenPerms)
		if len(result) != 0 {
			t.Errorf("expected empty result for no overlap, got %v", result)
		}
	})
}
