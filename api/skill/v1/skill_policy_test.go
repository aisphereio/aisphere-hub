package skillv1

import "testing"

func TestGitNativeSkillContractUsesCanonicalPermissions(t *testing.T) {
	tests := map[string]struct {
		permission string
		resource   string
	}{
		"/skill.v1.SkillService/CreateSkill":       {permission: "write", resource: "project:{org_id}/{project_id}"},
		"/skill.v1.SkillService/UpdateSkill":       {permission: "edit", resource: "skill:{name}"},
		"/skill.v1.SkillService/GetSkill":          {permission: "view", resource: "skill:{name}"},
		"/skill.v1.SkillService/DeleteSkill":       {permission: "manage", resource: "skill:{name}"},
		"/skill.v1.SkillService/CreateSkillShare":  {permission: "manage", resource: "skill:{name}"},
		"/skill.v1.SkillService/CreatePullRequest": {permission: "edit", resource: "skill:{name}"},
		"/skill.v1.SkillService/GetPullRequest":    {permission: "view", resource: "skill:{name}"},
		"/skill.v1.SkillService/ReviewPullRequest": {permission: "review", resource: "skill:{name}"},
		"/skill.v1.SkillService/MergePullRequest":  {permission: "publish", resource: "skill:{name}"},
		"/skill.v1.SkillService/ListSkillReleases": {permission: "view", resource: "skill:{name}"},
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

func TestGitNativeSkillContractRemovesLegacyContentLifecycle(t *testing.T) {
	legacy := map[string]bool{
		"UploadSkillPackage":        true,
		"ListSkillVersions":         true,
		"GetSkillVersion":           true,
		"SubmitSkillVersion":        true,
		"PublishSkillVersion":       true,
		"OnlineSkillVersion":        true,
		"OfflineSkillVersion":       true,
		"DownloadSkillVersion":      true,
		"ListSkillVersionFiles":     true,
		"GetSkillVersionFile":       true,
		"ListSkillDraftFiles":       true,
		"GetSkillDraftFile":         true,
		"UpsertSkillDraftFile":      true,
		"UpsertSkillDraftDirectory": true,
		"DeleteSkillDraftPath":      true,
		"MoveSkillDraftPath":        true,
		"CommitSkillDraft":          true,
	}
	for _, method := range SkillService_ServiceDesc.Methods {
		if legacy[method.MethodName] {
			t.Errorf("legacy method %s is still generated", method.MethodName)
		}
	}
}
