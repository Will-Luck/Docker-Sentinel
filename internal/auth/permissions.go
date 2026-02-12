package auth

// Built-in role IDs.
const (
	RoleAdminID    = "admin"
	RoleOperatorID = "operator"
	RoleViewerID   = "viewer"
)

// BuiltinRoles returns the three default roles.
func BuiltinRoles() []Role {
	return []Role{
		{
			ID:          RoleAdminID,
			Name:        "Admin",
			Permissions: AllPermissions(),
			BuiltIn:     true,
		},
		{
			ID:   RoleOperatorID,
			Name: "Operator",
			Permissions: []Permission{
				PermContainersView, PermContainersUpdate, PermContainersApprove,
				PermContainersRollback, PermContainersManage, PermSettingsView,
				PermLogsView, PermHistoryView,
			},
			BuiltIn: true,
		},
		{
			ID:   RoleViewerID,
			Name: "Viewer",
			Permissions: []Permission{
				PermContainersView, PermSettingsView, PermLogsView, PermHistoryView,
			},
			BuiltIn: true,
		},
	}
}

// ResolvePermissions returns the effective permissions for a user given their role.
// If the role has permissions, those are used. APIToken permissions (if non-nil) restrict further.
func ResolvePermissions(role *Role, tokenPerms []Permission) []Permission {
	if role == nil {
		return nil
	}
	rolePerms := role.Permissions
	if tokenPerms == nil {
		return rolePerms
	}
	// Intersect: only grant permissions that exist in both the role and the token scope.
	allowed := make(map[Permission]bool)
	for _, p := range rolePerms {
		allowed[p] = true
	}
	var result []Permission
	for _, p := range tokenPerms {
		if allowed[p] {
			result = append(result, p)
		}
	}
	return result
}
