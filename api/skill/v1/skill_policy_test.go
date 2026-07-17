package skillv1

import "testing"

func TestSkillAuthzRulesUseIAMSchemaPermissions(t *testing.T) {
	tests := map[string]struct {
		permission string
		resource   string
	}{
		"/skill.v1.SkillService/UpdateSkill":           {permission: "update", resource: "aihub:skill:{name}"},
		"/skill.v1.SkillService/PublishSkillVersion":   {permission: "publish", resource: "aihub:skill:{name}:version:{version}"},
		"/skill.v1.SkillService/UpdateSkillVisibility": {permission: "visibility:update", resource: "aihub:skill:{name}"},
		"/skill.v1.SkillService/CreateSkillShare":      {permission: "share:create", resource: "aihub:skill:{name}"},
		"/skill.v1.SkillService/DeleteSkill":           {permission: "delete", resource: "aihub:skill:{name}"},
	}
	for operation, want := range tests {
		rule, ok := SkillServiceAuthzRules[operation]
		if !ok {
			t.Fatalf("missing authz rule for %s", operation)
		}
		if rule.Action != want.permission || rule.Resource != want.resource {
			t.Errorf("%s = %s %s, want %s %s", operation, rule.Action, rule.Resource, want.permission, want.resource)
		}
	}
}
