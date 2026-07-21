package data

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"testing"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/kernel/kubernetesx"
	"github.com/aisphereio/kernel/logx"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newCredentialTestDB opens an in-memory SQLite GORM DB and auto-migrates the
// k8sCredentialModel table. SQLite stores []byte as BLOB, so AEAD ciphertext
// round-trips correctly without needing a real PostgreSQL. The migration's
// CHECK/UNIQUE/FK constraints are PostgreSQL-specific and not exercised here;
// migration_contract_test.go guards those against the real DDL.
func newCredentialTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{TranslateError: true})
	if err != nil {
		t.Fatalf("gorm.Open() error = %v", err)
	}
	if err := db.AutoMigrate(&k8sCredentialModel{}); err != nil {
		t.Fatalf("AutoMigrate() error = %v", err)
	}
	return db
}

// testEncryptionConfig returns a valid EncryptionConfig with a single 32-byte
// AES-256 key under version "v1". The key is random per test run so a leaked
// ciphertext from one test cannot be decrypted in another.
func testEncryptionConfig(t *testing.T) conf.EncryptionConfig {
	t.Helper()
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return conf.EncryptionConfig{
		MasterKeys:    map[string]string{"v1": base64.StdEncoding.EncodeToString(key)},
		ActiveVersion: "v1",
	}
}

// testServiceAccountCredential is a representative credential for round-trip
// tests: Kind + Host + Token + CACert, the fields the kernel SA branch reads
// (PR #36 TestConfigMergeCredential_ServiceAccountCarriesTokenAndCA).
func testServiceAccountCredential() kubernetesx.Credential {
	return kubernetesx.Credential{
		Kind:   kubernetesx.CredentialKindServiceAccount,
		Host:   "https://10.0.0.1:6443",
		Token:  "sa-token-secret-value",
		CACert: []byte("-----BEGIN CERTIFICATE-----\nfake-ca\n-----END CERTIFICATE-----\n"),
	}
}

func TestAEAD_PutGetRoundtrip(t *testing.T) {
	db := newCredentialTestDB(t)
	store, err := NewCredentialStore(func(context.Context) *gorm.DB { return db }, testEncryptionConfig(t), logx.DefaultLogger())
	if err != nil {
		t.Fatalf("NewCredentialStore() error = %v", err)
	}
	ctx := context.Background()
	cred := testServiceAccountCredential()
	locator, err := store.Put(ctx, "cluster-1", 1, cred)
	if err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if locator.CredentialRef == "" {
		t.Fatal("Put() returned empty ref")
	}
	if locator.CredentialRevision != 1 {
		t.Fatalf("revision = %d, want 1", locator.CredentialRevision)
	}
	got, err := store.Get(ctx, locator)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Kind != cred.Kind {
		t.Errorf("Kind = %q, want %q", got.Kind, cred.Kind)
	}
	if got.Host != cred.Host {
		t.Errorf("Host = %q, want %q", got.Host, cred.Host)
	}
	if got.Token != cred.Token {
		t.Errorf("Token = %q, want %q (plaintext leaked or mangled)", got.Token, cred.Token)
	}
	if !bytes.Equal(got.CACert, cred.CACert) {
		t.Errorf("CACert mismatch: got %q, want %q", got.CACert, cred.CACert)
	}
}

func TestAEAD_PutGeneratesUniqueRef(t *testing.T) {
	db := newCredentialTestDB(t)
	store, _ := NewCredentialStore(func(context.Context) *gorm.DB { return db }, testEncryptionConfig(t), logx.DefaultLogger())
	ctx := context.Background()
	cred := testServiceAccountCredential()
	loc1, _ := store.Put(ctx, "cluster-1", 1, cred)
	loc2, _ := store.Put(ctx, "cluster-1", 2, cred)
	if loc1.CredentialRef == loc2.CredentialRef {
		t.Fatal("two Put calls produced the same ref; refs must be unique")
	}
}

func TestAEAD_GetRejectsTamperedAAD(t *testing.T) {
	db := newCredentialTestDB(t)
	store, _ := NewCredentialStore(func(context.Context) *gorm.DB { return db }, testEncryptionConfig(t), logx.DefaultLogger())
	ctx := context.Background()
	cred := testServiceAccountCredential()
	locator, _ := store.Put(ctx, "cluster-1", 1, cred)

	// Tamper the locator's revision: the row exists (ref matches) but the
	// AAD reconstructed from the tampered locator will not match the stored
	// ciphertext. The drift check fires first (db row revision=1, locator
	// revision=2), returning ErrClusterFailedPrecondition.
	tampered := locator
	tampered.CredentialRevision = 999
	_, err := store.Get(ctx, tampered)
	if err == nil {
		t.Fatal("Get with tampered revision succeeded; want ErrClusterFailedPrecondition")
	}
	if !isFailedPrecondition(err) {
		t.Fatalf("Get tampered revision error = %v, want ErrClusterFailedPrecondition", err)
	}

	// Tamper the cluster_id: same drift path, different field.
	tamperedCluster := locator
	tamperedCluster.ClusterID = "cluster-other"
	_, err = store.Get(ctx, tamperedCluster)
	if err == nil || !isFailedPrecondition(err) {
		t.Fatalf("Get tampered cluster_id error = %v, want ErrClusterFailedPrecondition", err)
	}
}

func TestAEAD_GetRejectsDBRowDrift(t *testing.T) {
	db := newCredentialTestDB(t)
	store, _ := NewCredentialStore(func(context.Context) *gorm.DB { return db }, testEncryptionConfig(t), logx.DefaultLogger())
	ctx := context.Background()
	cred := testServiceAccountCredential()
	locator, _ := store.Put(ctx, "cluster-1", 1, cred)

	// Mutate the DB row's cluster_id directly, simulating a corrupted or
	// migrated row. The locator is unchanged (still "cluster-1"); the drift
	// check (row cluster_id="cluster-hacked" != locator cluster_id="cluster-1")
	// must reject before any decryption attempt.
	if err := db.Model(&k8sCredentialModel{}).
		Where("ref = ?", locator.CredentialRef).
		Update("cluster_id", "cluster-hacked").Error; err != nil {
		t.Fatalf("tamper DB row: %v", err)
	}
	_, err := store.Get(ctx, locator)
	if err == nil || !isFailedPrecondition(err) {
		t.Fatalf("Get after DB drift error = %v, want ErrClusterFailedPrecondition", err)
	}
}

func TestAEAD_RotateKeyReencrypts(t *testing.T) {
	db := newCredentialTestDB(t)
	// Two key versions: v1 (current) and v2 (target).
	keyV1 := make([]byte, 32)
	keyV2 := make([]byte, 32)
	io.ReadFull(rand.Reader, keyV1)
	io.ReadFull(rand.Reader, keyV2)
	cfg := conf.EncryptionConfig{
		MasterKeys: map[string]string{
			"v1": base64.StdEncoding.EncodeToString(keyV1),
			"v2": base64.StdEncoding.EncodeToString(keyV2),
		},
		ActiveVersion: "v1",
	}
	store, _ := NewCredentialStore(func(context.Context) *gorm.DB { return db }, cfg, logx.DefaultLogger())
	ctx := context.Background()
	cred := testServiceAccountCredential()
	loc1, _ := store.Put(ctx, "cluster-1", 1, cred)
	loc2, _ := store.Put(ctx, "cluster-1", 2, cred)

	// Before rotation: decrypting with the old key (v1) directly works, and
	// the store (active=v1) can Get. Decrypting with v2 directly fails.
	assertDirectDecrypt(t, db, keyV1, loc1, true, "v1 should decrypt loc1 before rotation")
	assertDirectDecrypt(t, db, keyV2, loc1, false, "v2 should NOT decrypt loc1 before rotation")

	n, err := store.RotateKey(ctx, "v1", "v2")
	if err != nil {
		t.Fatalf("RotateKey() error = %v", err)
	}
	if n != 2 {
		t.Fatalf("RotateKey reencrypted %d rows, want 2", n)
	}

	// After rotation: every row's key_version is now v2, so the store (which
	// still has both keys loaded) can Get. Direct decrypt with v1 must now
	// FAIL (ciphertext was re-encrypted under v2); direct decrypt with v2
	// must SUCCEED.
	assertDirectDecrypt(t, db, keyV2, loc1, true, "v2 should decrypt loc1 after rotation")
	assertDirectDecrypt(t, db, keyV1, loc1, false, "v1 should NOT decrypt loc1 after rotation")

	// The store-level Get still works (it dispatches on row.key_version).
	for _, loc := range []biz.CredentialLocator{loc1, loc2} {
		got, err := store.Get(ctx, loc)
		if err != nil {
			t.Fatalf("Get(%s) after rotation error = %v", loc.CredentialRef, err)
		}
		if got.Token != cred.Token {
			t.Errorf("token after rotation = %q, want %q", got.Token, cred.Token)
		}
	}

	// RotateKey again from v1 → v2 reencrypts 0 rows (none left at v1).
	n2, err := store.RotateKey(ctx, "v1", "v2")
	if err != nil {
		t.Fatalf("second RotateKey() error = %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second RotateKey reencrypted %d, want 0", n2)
	}
}

func TestAEAD_DeleteIsIdempotent(t *testing.T) {
	db := newCredentialTestDB(t)
	store, _ := NewCredentialStore(func(context.Context) *gorm.DB { return db }, testEncryptionConfig(t), logx.DefaultLogger())
	ctx := context.Background()
	cred := testServiceAccountCredential()
	loc, _ := store.Put(ctx, "cluster-1", 1, cred)

	if err := store.Delete(ctx, loc.CredentialRef); err != nil {
		t.Fatalf("first Delete() error = %v", err)
	}
	// Second delete of the same ref must not error (outbox retry path,
	// design §5.7.3 step 6).
	if err := store.Delete(ctx, loc.CredentialRef); err != nil {
		t.Fatalf("second Delete() error = %v; want idempotent", err)
	}
}

func TestNewCredentialStore_RejectsPlaceholderAndShortKey(t *testing.T) {
	db := newCredentialTestDB(t)
	getDB := func(context.Context) *gorm.DB { return db }

	// Placeholder "<from-env>" must be rejected at construction so Hub never
	// silently writes plaintext or crashes on first Put.
	_, err := NewCredentialStore(getDB, conf.EncryptionConfig{
		MasterKeys:    map[string]string{"v1": "<from-env>"},
		ActiveVersion: "v1",
	}, logx.DefaultLogger())
	if err == nil {
		t.Fatal("NewCredentialStore accepted placeholder key; want error")
	}

	// Short key (16 bytes, AES-128) must be rejected: V1 mandates AES-256.
	shortKey := base64.StdEncoding.EncodeToString(make([]byte, 16))
	_, err = NewCredentialStore(getDB, conf.EncryptionConfig{
		MasterKeys:    map[string]string{"v1": shortKey},
		ActiveVersion: "v1",
	}, logx.DefaultLogger())
	if err == nil {
		t.Fatal("NewCredentialStore accepted 16-byte key; want AES-256 error")
	}

	// active_version pointing at a non-existent version must be rejected.
	fullKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	_, err = NewCredentialStore(getDB, conf.EncryptionConfig{
		MasterKeys:    map[string]string{"v1": fullKey},
		ActiveVersion: "v2",
	}, logx.DefaultLogger())
	if err == nil {
		t.Fatal("NewCredentialStore accepted active_version not in master_keys; want error")
	}
}

// isFailedPrecondition reports whether err wraps biz.ErrClusterFailedPrecondition.
// The store uses fmt.Errorf("%w: ...", biz.ErrClusterFailedPrecondition, ...) so
// errors.Is chains down to the sentinel.
func isFailedPrecondition(err error) bool {
	return errors.Is(err, biz.ErrClusterFailedPrecondition)
}

// assertDirectDecrypt reaches into the DB row directly, reconstructs the AAD
// from the locator, and tries to decrypt with the supplied key. This bypasses
// the store's key-version dispatch to prove which key the ciphertext is
// actually encrypted under. wantOK=true means the decryption must succeed.
func assertDirectDecrypt(t *testing.T, db *gorm.DB, key []byte, loc biz.CredentialLocator, wantOK bool, msg string) {
	t.Helper()
	var row k8sCredentialModel
	if err := db.Where("ref = ?", loc.CredentialRef).First(&row).Error; err != nil {
		t.Fatalf("%s: load row: %v", msg, err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("%s: aes.NewCipher: %v", msg, err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatalf("%s: cipher.NewGCM: %v", msg, err)
	}
	aad := buildAAD(loc.ClusterID, loc.CredentialRef, loc.CredentialRevision)
	_, err = gcm.Open(nil, row.Nonce, row.Ciphertext, aad)
	gotOK := err == nil
	if gotOK != wantOK {
		t.Fatalf("%s: direct decrypt gotOK=%v, want %v (err=%v)", msg, gotOK, wantOK, err)
	}
}
