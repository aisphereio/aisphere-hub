package biz

import (
	"net/url"
	"strconv"
	"testing"
	"time"
)

func TestSkillPackageSigning(t *testing.T) {
	svc := NewSkillPackageService(nil, "test-secret", 5*time.Minute).(*skillPackageService)
	raw, err := svc.BuildDownloadURL("k8s-debug", "v1.2.0", "rt-1")
	if err != nil {
		t.Fatalf("BuildDownloadURL: %v", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if err := svc.VerifyDownloadURL("k8s-debug", "v1.2.0", "rt-1", q.Get("exp"), q.Get("sig")); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// tampered
	if err := svc.VerifyDownloadURL("k8s-debug", "v1.9.9", "rt-1", q.Get("exp"), q.Get("sig")); err == nil {
		t.Fatal("tampered ref accepted")
	}
	// expired
	if err := svc.VerifyDownloadURL("k8s-debug", "v1.2.0", "rt-1", strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10), q.Get("sig")); err == nil {
		t.Fatal("expired URL accepted")
	}
}