# Hub authorization boundary

Hub is an IAM authorization **data-plane client**.

It may call IAM gRPC for:

- permission checks and batch checks;
- resource and subject lookup;
- Skill owner/share relationship writes and reads.

Hub must not:

- connect to SpiceDB directly in production;
- read, validate, publish, or replace the shared authorization schema;
- expose IAM's authorization control-plane service from the Hub API.

Use:

```yaml
security:
  authz:
    enabled: true
    provider: iam_grpc
    iam_grpc:
      endpoint: aisphere-iam.aisphere.svc.cluster.local:19080
      caller_service: aisphere-hub
```

IAM owns the SpiceDB schema and the authorization control plane. Kernel defines
provider-neutral runtime interfaces so Hub business code remains independent of
the IAM protobuf transport.

The IAM gRPC client forwards only stable authorization identity fields from the
current Principal: subject UUID, subject type, provider, organization ID and
project ID. Display profile fields such as name, username, email and phone are
not authorization facts and must not be copied into gRPC metadata. SpiceDB
decisions remain keyed by the subject UUID carried in the permission request.

Collection APIs must not turn wildcard resource syntax into a SpiceDB object
ID. `ListSkills` is declared as `AUTHORIZED` with `SELF_CHECK`: Hub loads the
candidate page, sends one IAM batch check for `view` on each concrete
`skill:{name}`, and returns only allowed rows. IAM or SpiceDB failures fail the
whole request closed.
