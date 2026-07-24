package service

import "github.com/aisphereio/aisphere-hub/internal/biz"

// filterSkillReleaseVersions is the SkillHub product boundary between native
// Git tags and user-visible versions. Repositories may keep operational tags,
// but only strict SemVer tags are exposed as releases. Canonicalization also
// keeps clients from having to handle both 1.2.3 and v1.2.3 spellings.
func filterSkillReleaseVersions(items []biz.SkillRelease) []biz.SkillRelease {
	out := make([]biz.SkillRelease, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		canonical, valid := biz.NormalizeReleaseVersion(items[i].Tag)
		if !valid {
			continue
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		item := items[i]
		item.Tag = canonical
		out = append(out, item)
	}
	return out
}
