package biz

import "testing"

// TestSkillNamePattern locks the unified Skill identifier rule:
// lowercase [a-z0-9] start, then [a-z0-9_-], max 63 chars — identical to the
// frontend RESOURCE_ID_REGEX. Dots (and uppercase/slashes) are rejected so the
// Skill name works unchanged as SpiceDB object_id and git repo name.
func TestSkillNamePattern(t *testing.T) {
valid := []string{
		"cccc", "voxcpm2-tts", "test_skill", "t", "a-1_b", "0x",
		"abcdefghijklmnopqrstuvwxyz0123456789_-abcdefghijklmnopqrstuvwxy", // 63 chars
	}
	invalid := []string{
		"e2e.permission-test", // dot — the BUG1 case
		"a.b", "TestSkill", "with/slash", "with space",
		"-leading", "_leading", "", "a"+string(rune(0xFF)),
		"abcdefghijklmnopqrstuvwxyz0123456789_-abcdefghijklmnopqrstuvwxyz", // 64 chars
	}
	for _, name := range valid {
		if !skillNamePattern.MatchString(name) {
			t.Errorf("skillNamePattern should accept %q", name)
		}
	}
	for _, name := range invalid {
		if skillNamePattern.MatchString(name) {
			t.Errorf("skillNamePattern should reject %q", name)
		}
	}
}