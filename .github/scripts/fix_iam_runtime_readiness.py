#!/usr/bin/env python3
from pathlib import Path

http_path = Path("internal/server/http.go")
content = http_path.read_text(encoding="utf-8")

import_marker = '''	"github.com/aisphereio/kernel/logx"
'''
if '"github.com/aisphereio/kernel/authz"' not in content:
    if import_marker not in content:
        raise RuntimeError("kernel logx import marker not found")
    content = content.replace(import_marker, '\t"github.com/aisphereio/kernel/authz"\n' + import_marker, 1)

old = '''		// SpiceDB check (when authz enabled). We call ReadSchema as the
		// liveness probe — it's a lightweight gRPC call that exercises
		// the full authz stack (connection, auth, schema service). A
		// failure here means either SpiceDB is down or the configured
		// token is wrong; either way the hub cannot serve authz-protected
		// requests.
		if resources.AuthzService != nil {
			if _, err := resources.AuthzService.ReadSchema(r.Context()); err != nil {
				checks["spicedb"] = "fail: " + err.Error()
				allReady = false
			} else {
				checks["spicedb"] = "ok"
			}
		}
'''
new = '''		// IAM authorization runtime check. Hub deliberately has no schema
		// administration capability, so readiness uses a side-effect-free
		// permission check instead of ReadSchema. A deny decision is healthy:
		// only transport/provider failures return an error. This exercises the
		// complete Hub -> IAM gRPC -> authorization provider path.
		if resources.AuthzService != nil {
			_, err := resources.AuthzService.Check(r.Context(), authz.CheckRequest{
				Subject:    authz.SubjectRef{Type: authz.SubjectTypeService, ID: "aisphere-hub"},
				Resource:   authz.ObjectRef{Type: "iam_authz", ID: "global"},
				Permission: "view_schema",
			})
			if err != nil {
				checks["iam_authz"] = "fail: " + err.Error()
				allReady = false
			} else {
				checks["iam_authz"] = "ok"
			}
		}
'''
if old not in content:
    raise RuntimeError("legacy SpiceDB readiness block not found")
http_path.write_text(content.replace(old, new, 1), encoding="utf-8")

docker_path = Path("Dockerfile")
docker = docker_path.read_text(encoding="utf-8")
if "ARG GO_VERSION=1.25.8" in docker:
    docker = docker.replace("ARG GO_VERSION=1.25.8", "ARG GO_VERSION=1.25.12", 1)
elif "ARG GO_VERSION=1.25.12" not in docker:
    raise RuntimeError("unexpected Dockerfile Go version")
docker_path.write_text(docker, encoding="utf-8")

print("IAM runtime readiness probe and patched Go builder applied")
