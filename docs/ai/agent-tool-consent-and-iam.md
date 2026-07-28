# Agent Tool Consent and IAM Enforcement

## Decision

AISphere separates **human consent** from **authorization**:

- An Agent version declares which Tool versions may be exposed to the model.
- Each Tool binding declares an approval mode: `always`, `per_run`, or `disabled`.
- Human approval controls whether a Tool is included in a concrete Runtime snapshot.
- Runtime only receives and propagates the authenticated Principal through the trusted internal context.
- Runtime does not mint delegated credentials, copy IAM policy, or decide resource authorization.
- The target resource service derives the real resource and action from the actual request, calls IAM, and executes or rejects the operation.

## Approval modes

| Mode | Meaning | Runtime snapshot |
| --- | --- | --- |
| `always` | The Agent creator/operator consents to expose this Tool whenever this Agent version runs. This is not an IAM grant. | Included automatically |
| `per_run` | The user launching the Agent must approve this Tool for the current run. | Included only when approved |
| `disabled` | The Tool is retained in the draft but unavailable to the model. | Never included |

A high-risk Tool such as `skill.publish` should normally use `per_run`. A local read-only Tool such as `workspace.read` can use `always`.

## Request flow

```text
Human Principal
  -> POST /v1/agents/{id}:plan-run
  -> Hub returns Tool approval plan and required IAM permission descriptions
  -> human approves selected per_run Tools
  -> POST /v1/agents/{id}:resolve
  -> Hub returns immutable Agent Runtime Snapshot containing only approved Tools
  -> Runtime compiles the snapshot into ADK Tool proxies
  -> Runtime propagates the same trusted Principal to the target service
  -> target service calls IAM CheckPermission for the concrete resource/action
  -> target service executes or rejects
```

The Runtime snapshot uses the policy marker:

```text
principal_passthrough_iam_enforced
```

and declares:

```json
{
  "principalPropagation": "trusted_internal_context",
  "iamEnforcement": "resource_service"
}
```

## Why the Hub does not pre-authorize concrete resources

At Agent creation time the target resource often does not exist yet. A binding such as `skill.publish` only describes the class of authority that may be requested. The concrete call later contains the actual Skill name and Git ref. The Hub therefore displays:

```text
resourceType = skill
permission   = publish
enforcement  = iam_at_resource_service
```

but does not persist a grant.

The Skill/Git service extracts the real repository and ref, asks IAM whether the Principal has `publish` on that Skill, then applies the decision. The same IAM path is used whether the caller is an Agent Runtime, the Web UI, or `aisphere-git-cli`.

## Security invariants

1. Agent Tool binding is capability assembly, not authorization.
2. Human consent cannot grant a permission the Principal does not already have in IAM.
3. Runtime cannot enlarge or forge Principal identity.
4. Sandbox cannot inject trusted Principal headers.
5. Resource services fail closed when IAM is unavailable.
6. The concrete operation is checked at the point of enforcement, including Git repository and ref-level details.
7. A Runtime snapshot is immutable and records exactly which Tool versions were approved.
