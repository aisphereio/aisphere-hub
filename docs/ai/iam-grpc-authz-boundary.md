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
