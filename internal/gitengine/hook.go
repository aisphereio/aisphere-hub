package gitengine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	iamauthz "github.com/aisphereio/aisphere-iam/client/authzgrpc"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/authz"
)

type permissionChecker interface {
	Check(context.Context, authz.CheckRequest) (authz.Decision, error)
}

// RunHook is the hidden Git hook entrypoint executed by generated Soft Serve
// repository hooks. Only update is enforcing; the other standard hook names
// remain successful so Soft Serve's hook fan-out continues to work.
func RunHook(ctx context.Context, args []string, _ io.Reader, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "update" {
		return 0
	}
	if len(args) != 4 {
		_, _ = fmt.Fprintln(stderr, "skillhub: update hook expects ref old-sha new-sha")
		return 2
	}
	principal := hookPrincipal()
	if !principal.IsAuthenticated() {
		_, _ = fmt.Fprintln(stderr, "skillhub: authenticated principal missing from push")
		return 1
	}
	repository := strings.TrimSpace(os.Getenv("SOFT_SERVE_REPO_NAME"))
	if repository == "" {
		_, _ = fmt.Fprintln(stderr, "skillhub: repository identity missing from push")
		return 1
	}
	client, err := iamauthz.New(iamauthz.Config{
		Endpoint:      strings.TrimSpace(os.Getenv("AISPHERE_IAM_AUTHZ_ENDPOINT")),
		CallerService: strings.TrimSpace(os.Getenv("AISPHERE_IAM_CALLER_SERVICE")),
		Insecure:      envBool("AISPHERE_IAM_AUTHZ_INSECURE"),
	})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "skillhub: initialize IAM authorization: %v\n", err)
		return 1
	}
	defer client.Close()
	fastForward, err := hookFastForward(ctx, args[2], args[3])
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "skillhub: inspect ref update: %v\n", err)
		return 1
	}
	permission := RequiredPermissionForRefUpdate(os.Getenv("AISPHERE_GIT_DEFAULT_BRANCH"), args[1], args[2], args[3], fastForward)
	if err := authorizeRefUpdate(ctx, client, principal, repository, permission); err != nil {
		_, _ = fmt.Fprintf(stderr, "skillhub: %v\n", err)
		return 1
	}
	if strings.TrimPrefix(normalizeRef(args[1]), "refs/heads/") == defaultBranch() && !isZero(args[3]) {
		repoPath := strings.TrimSpace(os.Getenv("SOFT_SERVE_REPO_PATH"))
		content, err := runGitRepo(ctx, repoPath, nil, "show", args[3]+":SKILL.md")
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "skillhub: read default-branch SKILL.md: %v\n", err)
			return 1
		}
		if _, _, err := ParseSkillMetadata(repository, content); err != nil {
			_, _ = fmt.Fprintf(stderr, "skillhub: invalid default-branch metadata: %v\n", err)
			return 1
		}
	}
	return 0
}

func defaultBranch() string {
	branch := strings.TrimSpace(os.Getenv("AISPHERE_GIT_DEFAULT_BRANCH"))
	if branch == "" {
		return biz.SkillDefaultBranch
	}
	return strings.TrimPrefix(branch, "refs/heads/")
}

func authorizeRefUpdate(ctx context.Context, checker permissionChecker, principal authn.Principal, repository, permission string) error {
	decision, err := checker.Check(authn.ContextWithPrincipal(ctx, principal), authz.CheckRequest{
		Subject:    authz.SubjectRef{Type: principal.SubjectType, ID: principal.SubjectID},
		Resource:   authz.ObjectRef{Type: "skill", ID: repository},
		Permission: permission,
		TenantID:   principal.TenantID,
		OrgID:      principal.OrgID,
	})
	if err != nil {
		return fmt.Errorf("authorize %s on skill %s: %w", permission, repository, err)
	}
	if !decision.Allowed {
		return fmt.Errorf("permission %s denied on skill %s", permission, repository)
	}
	return nil
}

func hookPrincipal() authn.Principal {
	return authn.Principal{
		SubjectID:   strings.TrimSpace(os.Getenv("AISPHERE_PRINCIPAL_ID")),
		SubjectType: strings.TrimSpace(os.Getenv("AISPHERE_PRINCIPAL_TYPE")),
		TenantID:    strings.TrimSpace(os.Getenv("AISPHERE_PRINCIPAL_TENANT_ID")),
		OrgID:       strings.TrimSpace(os.Getenv("AISPHERE_PRINCIPAL_ORG_ID")),
		Provider:    "skillhub-git-hook",
	}.Normalize()
}

func hookFastForward(ctx context.Context, oldSHA, newSHA string) (bool, error) {
	if isZero(oldSHA) || isZero(newSHA) {
		return false, nil
	}
	repoPath := strings.TrimSpace(os.Getenv("SOFT_SERVE_REPO_PATH"))
	if repoPath == "" {
		return false, fmt.Errorf("repository path is missing")
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repoPath, "merge-base", "--is-ancestor", oldSHA, newSHA)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func envBool(name string) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return value
}
