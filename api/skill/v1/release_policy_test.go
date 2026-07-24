package skillv1

import "testing"

func TestSkillReleaseContractUsesCanonicalPermissions(t *testing.T) {
	tests := map[string]struct {
		permission string
		resource   string
	}{
		"/skill.v1.SkillReleaseService/ResolveSkillRef":     {permission: "view", resource: "skill:{name}"},
		"/skill.v1.SkillReleaseService/CreateSkillRelease":  {permission: "publish", resource: "skill:{name}"},
		"/skill.v1.SkillReleaseService/GetSkillRelease":     {permission: "view", resource: "skill:{name}"},
		"/skill.v1.SkillReleaseService/ResolveSkillRelease": {permission: "view", resource: "skill:{name}"},
	}
	for operation, want := range tests {
		rule, ok := SkillReleaseServiceAuthzRules[operation]
		if !ok {
			t.Fatalf("missing authz rule for %s", operation)
		}
		if rule.Action != want.permission || rule.Resource != want.resource {
			t.Errorf("%s = %s %s, want %s %s", operation, rule.Action, rule.Resource, want.permission, want.resource)
		}
	}
}
