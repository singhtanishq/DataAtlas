package auth

import "testing"

func TestAuthorizeMatrix(t *testing.T) {
	cases := []struct {
		role       Role
		permission Permission
		want       bool
	}{
		{RoleOwner, PermManageOrganization, true},
		{RoleOwner, PermManageMembers, true},
		{RoleOwner, PermManageSources, true},
		{RoleOwner, PermRead, true},
		{RoleAdmin, PermManageOrganization, false},
		{RoleAdmin, PermManageMembers, true},
		{RoleAdmin, PermManageAPIKeys, true},
		{RoleDataSteward, PermManageQuality, true},
		{RoleDataSteward, PermManageMembers, false},
		{RoleDataEngineer, PermManageSources, true},
		{RoleDataEngineer, PermEditMetadata, true},
		{RoleDataEngineer, PermManageQuality, false},
		{RoleAnalyst, PermRead, true},
		{RoleAnalyst, PermEditMetadata, false},
		{RoleAuditor, PermRead, true},
		{RoleAuditor, PermEditMetadata, false},
		{RoleMember, PermRead, true},
		{RoleMember, PermManageSources, false},
	}
	for _, tc := range cases {
		if got := Authorize(tc.role, tc.permission); got != tc.want {
			t.Errorf("Authorize(%s, %s) = %v; want %v", tc.role, tc.permission, got, tc.want)
		}
	}
}

func TestValidateRole(t *testing.T) {
	if err := ValidateRole("OWNER"); err != nil {
		t.Errorf("OWNER should be valid: %v", err)
	}
	if err := ValidateRole("SUPERADMIN"); err == nil {
		t.Error("SUPERADMIN should be invalid")
	}
}
