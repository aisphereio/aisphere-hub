package service

import "github.com/aisphereio/aisphere-hub/internal/biz"

// filterSkillReleaseVersions is the SkillHub product boundary between native
// Git tags and user-visible versions. Repositories may keep operational tags,
// but only canonical strict-SemVer tags (vMAJOR.MINOR.PATCH) are exposed as
// releases. CreateRelease always writes this canonical form; filtering legacy
// no-v tags avoids returning a version whose exact Git ref cannot be resolved
// through the canonical release API.
func filterSkillReleaseVersions(items []biz.SkillRelease) []biz.SkillRelease {
	out := make([]biz.SkillRelease, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i := range items {
		canonical, valid := biz.NormalizeReleaseVersion(items[i].Tag)
		if !valid || items[i].Tag != canonical {
			continue
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		out = append(out, items[i])
	}
	return out
}
