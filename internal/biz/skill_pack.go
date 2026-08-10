package biz

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/aisphereio/kernel/errorx"
)

// SkillPackageData is the immutable skill package content (zip of the tree at
// a release tag) plus package-level digests. Runtime verifies SHA256 after
// download; the package is then treated as local content (no re-check).
type SkillPackageData struct {
	SHA256 string `json:"sha256"`
	MD5    string `json:"md5"`
	Size   int64  `json:"size"`
	ZIP    []byte `json:"-"`
}

// SkillPackageService builds and signs immutable skill packages. The download
// URL it produces is the *load-phase authorization*: the URL is bound to the
// runtime/session and expires, so a principal who cannot resolve the skill
// (no skill:view) can never obtain a fetchable package.
type SkillPackageService interface {
	BuildSkillPackage(ctx context.Context, name, ref string) (*SkillPackageData, error)
	// BuildDownloadURL returns /v1/skills/{name}/packages?ref=&exp=&rt=&sig=.
	BuildDownloadURL(name, version, runtimeID string) (string, error)
	// VerifyDownloadURL validates 签名+expiry for the download endpoint.
	VerifyDownloadURL(name, ref, runtimeID, exp, sig string) error
}

type skillPackageService struct {
	git    SkillGitEngine
	secret string
	ttl    time.Duration
}

// NewSkillPackageService wires package building onto the git engine and the
// HMAC signing key (conf Skill.Pack.Secret).
func NewSkillPackageService(git SkillGitEngine, secret string, ttl time.Duration) SkillPackageService {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &skillPackageService{git: git, secret: secret, ttl: ttl}
}

func (s *skillPackageService) BuildSkillPackage(ctx context.Context, name, ref string) (*SkillPackageData, error) {
	return s.git.BuildSkillPackage(ctx, name, ref)
}

func (s *skillPackageService) BuildDownloadURL(name, version, runtimeID string) (string, error) {
	exp := time.Now().Add(s.ttl).Unix()
	q := fmt.Sprintf("name=%s&ref=%s&rt=%s&exp=%d", url.QueryEscape(name), url.QueryEscape(version), url.QueryEscape(runtimeID), exp)
	sig := s.sign(fmt.Sprintf("%s|%s|%s|%d", name, version, runtimeID, exp))
	return fmt.Sprintf("/v1/skills/%s/packages?%s&sig=%s", url.PathEscape(name), q, sig), nil
}

func (s *skillPackageService) VerifyDownloadURL(name, ref, runtimeID, expRaw, sig string) error {
	if s.secret == "" {
		return ErrSkillDependencyFailed
	}
	exp, err := strconv.ParseInt(expRaw, 10, 64)
	if err != nil || exp < time.Now().Unix() {
		return errSkillPackExpired()
	}
	expect := s.sign(fmt.Sprintf("%s|%s|%s|%d", name, ref, runtimeID, exp))
	if !hmac.Equal([]byte(expect), []byte(sig)) {
		return errSkillPackForbidden()
	}
	return nil
}

func (s *skillPackageService) sign(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func errSkillPackExpired() error {
	return errorx.BadRequest("SKILL_PACK_EXPIRED", "skill package download URL expired")
}

func errSkillPackForbidden() error {
	return errorx.BadRequest("SKILL_PACK_FORBIDDEN", "invalid skill package download signature")
}