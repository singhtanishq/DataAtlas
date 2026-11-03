package auth

import "fmt"

// Role is the fixed set of organization roles (Part 24.1).
type Role string

const (
	RoleOwner        Role = "OWNER"
	RoleAdmin        Role = "ADMIN"
	RoleDataSteward  Role = "DATA_STEWARD"
	RoleDataEngineer Role = "DATA_ENGINEER"
	RoleAnalyst      Role = "ANALYST"
	RoleAuditor      Role = "AUDITOR"
	RoleMember       Role = "MEMBER"
)

// Roles ordered by decreasing privilege.
var roleRank = map[Role]int{
	RoleOwner:        7,
	RoleAdmin:        6,
	RoleDataSteward:  5,
	RoleDataEngineer: 4,
	RoleAnalyst:      3,
	RoleAuditor:      3,
	RoleMember:       2,
}

// Permission names used across the API surface.
const (
	PermManageOrganization = "organization:manage" // settings, deletion, member management
	PermManageMembers      = "members:manage"      // invite, role assignment, removal
	PermManageSources      = "sources:manage"      // register, edit, remove data sources
	PermManageQuality      = "quality:manage"      // create/edit quality checks
	PermEditMetadata       = "metadata:edit"       // descriptions, ownership, tags, glossary
	PermTriggerIngestion   = "ingestion:trigger"   // run ingestion pipelines
	PermRead               = "read"                // read catalog, quality, lineage
	PermManageAPIKeys      = "api-keys:manage"
)

// minRoleForPermission maps each permission to the minimum role rank that
// holds it. AUDITOR holds read-only parity with ANALYST by rank.
var minRoleForPermission = map[Permission]Role{
	PermRead:               RoleMember,
	PermEditMetadata:       RoleDataEngineer,
	PermTriggerIngestion:   RoleDataEngineer,
	PermManageQuality:      RoleDataSteward,
	PermManageSources:      RoleDataEngineer,
	PermManageAPIKeys:      RoleAdmin,
	PermManageMembers:      RoleAdmin,
	PermManageOrganization: RoleOwner,
}

// Permission is an authorization decision point.
type Permission string

// Authorize reports whether a role holds a permission.
func Authorize(role Role, permission Permission) bool {
	minRole, ok := minRoleForPermission[permission]
	if !ok {
		return false
	}
	rank, ok := roleRank[role]
	if !ok {
		return false
	}
	return rank >= roleRank[minRole]
}

// ValidateRole reports whether a role string is a known role.
func ValidateRole(s string) error {
	role := Role(s)
	if _, ok := roleRank[role]; !ok {
		return fmt.Errorf("unknown role %q", s)
	}
	return nil
}
