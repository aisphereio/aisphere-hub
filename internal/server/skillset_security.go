package server

import (
	"net/http"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/data"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authz"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

// registerSecuredSkillSetHTTP keeps SkillSet as a lightweight PostgreSQL-only
// Hub resource while putting its handwritten HTTP handlers on the same trusted
// principal and IAM authorization path as generated services.
func registerSecuredSkillSetHTTP(srv *khttp.Server, resources *data.Resources) {
	if srv == nil || resources == nil || resources.DB == nil {
		return
	}
	h := &skillSetHTTPHandler{resources: resources}
	srv.HandleFunc("/v1/skillsets", h.withSkillSetSecurity(h.root))
	srv.HandleFunc("/v1/skillsets/", h.withSkillSetSecurity(h.item))
	srv.HandleFunc("/v1/skills/", h.withSkillSetSecurity(h.reverseLookup))
}

// withSkillSetSecurity bridges the Kernel-authenticated Principal into the
// legacy SkillSet handler and enforces Zone membership for collection creation.
// Client supplied identity headers are always discarded or overwritten.
func (h *skillSetHTTPHandler) withSkillSetSecurity(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		principal, authenticated := authn.PrincipalFromContext(r.Context())

		cloned := r.Clone(r.Context())
		cloned.Header = r.Header.Clone()
		for _, name := range []string{
			"X-Aisphere-Principal",
			"X-Principal-Id",
			"X-User-Id",
			"X-Aisphere-Org",
			"X-Org-Id",
		} {
			cloned.Header.Del(name)
		}
		if authenticated {
			cloned.Header.Set("X-Aisphere-Principal", principal.SubjectID)
			cloned.Header.Set("X-Aisphere-Org", principal.OrgID)
		}

		if cloned.Method == http.MethodPost && strings.TrimRight(cloned.URL.Path, "/") == "/v1/skillsets" {
			if !authenticated {
				writeJSON(w, http.StatusUnauthorized, map[string]string{
					"code":    "UNAUTHENTICATED",
					"message": "authentication required",
				})
				return
			}
			if strings.TrimSpace(principal.OrgID) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"code":    "SKILLSET_ZONE_REQUIRED",
					"message": "authenticated principal has no zone",
				})
				return
			}
			if h.resources.Authz == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"code":    "SKILLSET_AUTHZ_UNAVAILABLE",
					"message": "authorization service is unavailable",
				})
				return
			}

			subjectType := principal.SubjectType
			if strings.TrimSpace(subjectType) == "" {
				subjectType = authz.SubjectTypeUser
			}
			decision, err := h.resources.Authz.Check(cloned.Context(), authz.CheckRequest{
				Subject: authz.SubjectRef{
					Type: subjectType,
					ID:   principal.SubjectID,
				},
				Resource: authz.ObjectRef{
					Type: "zone",
					ID:   principal.OrgID,
				},
				Permission: "create_skill",
				OrgID:      principal.OrgID,
			})
			if err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"code":    "SKILLSET_AUTHZ_UNAVAILABLE",
					"message": "authorization check failed",
				})
				return
			}
			if !decision.IsAllowed() {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"code":    "SKILLSET_PERMISSION_DENIED",
					"message": "zone membership is required to create a skillset",
				})
				return
			}
		}

		next(w, cloned)
	}
}
