package conf

import "testing"

func TestValidateProductionSecurityFailsClosed(t *testing.T) {
	tests := map[string]SecurityConfig{
		"authn disabled": {Authz: AuthzConfig{Enabled: true, Provider: "iam_grpc"}},
		"authz disabled": {Authn: AuthnConfig{Enabled: true, Mode: "principal_jwt"}},
		"dev allow all": {
			Authn: AuthnConfig{Enabled: true, Mode: "principal_jwt"},
			Authz: AuthzConfig{Enabled: true, Provider: "iam_grpc", DevAllowAll: true},
		},
		"direct provider": {
			Authn: AuthnConfig{Enabled: true, Mode: "principal_jwt"},
			Authz: AuthzConfig{Enabled: true, Provider: "spicedb"},
		},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if err := ValidateProductionSecurity(ServiceConfig{Env: "production"}, cfg); err == nil {
				t.Fatal("expected fail-closed validation error")
			}
		})
	}
}
