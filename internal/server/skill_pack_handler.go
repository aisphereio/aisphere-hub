package server

import (
	"net/http"
	"strconv"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
	khttp "github.com/aisphereio/kernel/transportx/http"
)

// skillPackHTTPHandler serves the skill package download endpoint. It is the
// load-phase authorization point: only a signed + unexpired URL (produced by
// SkillPackageService during an authorized resolve) can fetch a package.
type skillPackHTTPHandler struct {
	pack biz.SkillPackageService
}

// registerSkillPackHTTP mounts GET /v1/skills/{name}/packages on the shared
// Kernel HTTP server (mirrors registerModelManagementHTTP).
func registerSkillPackHTTP(srv *khttp.Server, pack biz.SkillPackageService) {
	if srv == nil || pack == nil {
		return
	}
	h := &skillPackHTTPHandler{pack: pack}
	r := srv.Route("/")
	r.Handle(http.MethodGet, "/v1/skills/{name}/packages", h.download)
}

func (h *skillPackHTTPHandler) download(c khttp.Context) error {
	q := c.Query()
	name, ref := c.Vars().Get("name"), q.Get("ref")
	runtimeID, exp, sig := q.Get("rt"), q.Get("exp"), q.Get("sig")
	if name == "" || ref == "" || runtimeID == "" || exp == "" || sig == "" {
		return errorx.BadRequest("SKILL_PACK_INVALID_REQUEST", "missing skill package download parameters")
	}
	if err := h.pack.VerifyDownloadURL(name, ref, runtimeID, exp, sig); err != nil {
		return err
	}
	pkg, err := h.pack.BuildSkillPackage(c.Request().Context(), name, ref)
	if err != nil {
		return err
	}
	c.Response().Header().Set("X-Content-SHA256", pkg.SHA256)
	c.Response().Header().Set("Content-Length", strconv.Itoa(int(pkg.Size)))
	return c.Blob(http.StatusOK, "application/zip", pkg.ZIP)
}