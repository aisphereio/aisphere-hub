# Model Management V2

## 1. Decision

AISphere model management is split into three resources:

```text
Model
  -> ModelEndpoint
  -> ModelProfile
  -> Agent / Runtime
```

- **Model** is the AISphere-owned model identity. Its `id` is an internal UUID. It stores vendor-neutral capabilities and reasoning semantics.
- **ModelEndpoint** is one callable deployment or provider endpoint. It stores `base_url`, protocol, credential reference and `provider_model_id`.
- **ModelProfile** is the Agent-facing usage policy. It selects an Endpoint and defines limits, default parameters, Tool exposure and normalized reasoning policy.

`provider_model_id` is intentionally different from `model_id`:

```text
model_id
= AISphere Model UUID

provider_model_id
= value sent in the provider request's model field
```

The UI label for `provider_model_id` is **服务端模型 ID**.

## 2. Reasoning model

Reasoning is represented in two layers.

### 2.1 Provider-neutral capability on Model

```json
{
  "supported": true,
  "modes": ["auto", "enabled", "disabled"],
  "effortLevels": ["none", "minimal", "low", "medium", "high", "max"],
  "defaultMode": "auto",
  "defaultEffort": "medium",
  "supportsBudgetTokens": false,
  "preserveReasoningContent": true
}
```

This describes what the model supports. It does not contain provider request paths.

### 2.2 Provider-specific mapping on ModelEndpoint

Endpoint translates normalized Profile policy into actual request fields.

Qwen through vLLM/SGLang can use:

```json
{
  "strategy": "field_map",
  "modeField": "chat_template_kwargs.enable_thinking",
  "enabledValue": true,
  "disabledValue": false
}
```

DeepSeek-compatible endpoints can use:

```json
{
  "strategy": "field_map",
  "modeField": "thinking.type",
  "enabledValue": "enabled",
  "disabledValue": "disabled",
  "effortField": "reasoning_effort",
  "effortMap": {
    "low": "high",
    "medium": "high",
    "high": "high",
    "max": "max"
  },
  "responseField": "reasoning_content",
  "preserveOnTool": true
}
```

GLM-compatible endpoints use the same generic field-map shape, while keeping their own effort map and optional provider overrides.

No provider name is hard-coded in Runtime. Runtime consumes the resolved `providerRequestPatch` generated from the Endpoint mapping.

## 3. ModelProfile policy

Agent authors configure only normalized fields:

```json
{
  "mode": "inherit",
  "effort": "inherit",
  "budgetTokens": 0,
  "exposeReasoning": false,
  "providerOverrides": {}
}
```

Allowed modes:

- `inherit`: use Model defaults;
- `auto`: provider/model decides dynamically;
- `enabled`: force reasoning on;
- `disabled`: force reasoning off.

Canonical effort levels:

```text
none < minimal < low < medium < high < max
```

Endpoint maps these canonical values into provider-specific values. Unsupported modes, efforts or budget tokens are rejected when the Profile is saved.

## 4. Immutable Runtime snapshot

Every Profile execution-definition update creates an automatic integer revision. Users do not type revision labels.

The revision stores a fully resolved snapshot rather than only foreign keys:

```json
{
  "profile": {},
  "model": {},
  "endpoint": {},
  "reasoning": {
    "policy": {},
    "providerRequestPatch": {},
    "responseField": "reasoning_content",
    "preserveOnTool": true
  }
}
```

This keeps a running Agent reproducible even if the Model or Endpoint is later edited.

Runtime resolves:

```text
POST /v1/model-profiles/{profile_id}:resolve
```

and receives:

- `logicalName = aisphere://model-profiles/{code}`;
- internal Model, Endpoint and Profile IDs;
- provider model ID;
- protocol and base URL;
- credential reference, never the plaintext credential;
- normalized reasoning policy;
- provider request patch;
- immutable revision and SHA-256.

## 5. Ownership boundaries

```text
Hub
- Model / Endpoint / Profile catalog
- validation
- immutable snapshot
- permissions and audit

Runtime
- credential resolution
- provider adapter
- merge request defaults + Profile defaults + invocation overrides
- apply providerRequestPatch
- preserve provider reasoning fields during Tool continuation
- call model and record telemetry
```

The Agent and Sandbox never receive plaintext model credentials.

## 6. Authorization

Model catalog permissions are independent from the Skill domain:

```text
zone.use_models
zone.manage_models
```

- `use_models` allows catalog reads and resolving an active ModelProfile.
- `manage_models` allows Model, ModelEndpoint and ModelProfile mutations.

The permissions are defined by the companion IAM change `aisphere-iam#61` and flow through custom roles and role bindings.

## 7. Migration

The V2 implementation uses new tables:

- `aihub_models_v2`
- `aihub_model_endpoints_v2`
- `aihub_model_profiles_v2`
- `aihub_model_profile_revisions_v2`

The old flat ModelProfile HTTP contract is no longer mounted. The legacy generated gRPC service remains available temporarily for migration, but new clients must use the V2 HTTP resources.
