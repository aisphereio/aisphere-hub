package data

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/aisphere-hub/internal/conf"
	"github.com/aisphereio/kernel/kubernetesx"
	"github.com/aisphereio/kernel/logx"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// aeadCredentialStore implements biz.ClusterCredentialStore with versioned
// AEAD (design §5.5 V1). AES-256-GCM with a single master key per key_version;
// no DEK/KEK envelope, no KMS. AAD = cluster_id + ref + credential_revision,
// reconstructed on every Get from the caller-supplied Locator (never
// persisted), so ciphertext cannot be replayed against a different credential
// revision or cluster.
type aeadCredentialStore struct {
	db            func(context.Context) *gorm.DB
	keys          map[string][]byte // key_version -> 32-byte AES key
	activeVersion string
	logger        logx.Logger
}

// k8sCredentialModel mirrors the k8s_cluster_credentials table (migration
// 202607220001). ciphertext/nonce are BYTEA; key_version selects the master
// key; credential_revision is the credential version (AAD-bound), distinct
// from k8s_clusters.revision (resource CAS).
type k8sCredentialModel struct {
	Ref                string `gorm:"column:ref;primaryKey"`
	ClusterID          string `gorm:"column:cluster_id;not null"`
	CredentialRevision int64  `gorm:"column:credential_revision;not null"`
	Ciphertext         []byte `gorm:"column:ciphertext;not null"`
	Nonce              []byte `gorm:"column:nonce;not null"`
	KeyVersion         string `gorm:"column:key_version;not null"`
	CredentialType     string `gorm:"column:credential_type;not null"`
}

func (k8sCredentialModel) TableName() string { return "k8s_cluster_credentials" }

// NewCredentialStore builds the AEAD store from the encryption config. Master
// keys are base64-decoded from conf.EncryptionConfig.MasterKeys; the
// active_version key must be present and 32 bytes (AES-256). Returns an error
// if the config is incomplete — Hub startup must fail closed rather than
// silently writing plaintext or using a short key.
func NewCredentialStore(db func(context.Context) *gorm.DB, cfg conf.EncryptionConfig, logger logx.Logger) (biz.ClusterCredentialStore, error) {
	if len(cfg.MasterKeys) == 0 {
		return nil, errors.New("kubernetes encryption: no master keys configured")
	}
	if cfg.ActiveVersion == "" {
		return nil, errors.New("kubernetes encryption: active_version is empty")
	}
	keys := make(map[string][]byte, len(cfg.MasterKeys))
	for version, b64 := range cfg.MasterKeys {
		// "<from-env>" placeholder is rejected: the env overlay must inject a
		// real key before the store can be constructed.
		if b64 == "" || b64 == "<from-env>" {
			return nil, fmt.Errorf("kubernetes encryption: master key version %q is a placeholder; inject via env", version)
		}
		key, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("kubernetes encryption: master key version %q is not valid base64: %w", version, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("kubernetes encryption: master key version %q must be 32 bytes (AES-256), got %d", version, len(key))
		}
		keys[version] = key
	}
	if _, ok := keys[cfg.ActiveVersion]; !ok {
		return nil, fmt.Errorf("kubernetes encryption: active_version %q has no key in master_keys", cfg.ActiveVersion)
	}
	return &aeadCredentialStore{
		db:            db,
		keys:          keys,
		activeVersion: cfg.ActiveVersion,
		logger:        logger,
	}, nil
}

// Put allocates a fresh ref (UUID), encrypts value under the active master
// key with AAD = {clusterID, ref, credentialRevision}, and persists the row.
// Returns the Locator so biz can store credential_ref + credential_revision.
// credentialRevision is the *target* revision — Put does not increment it.
func (s *aeadCredentialStore) Put(ctx context.Context, clusterID string, credentialRevision int64, value kubernetesx.Credential) (biz.CredentialLocator, error) {
	ref, err := newCredentialRef()
	if err != nil {
		return biz.CredentialLocator{}, fmt.Errorf("generate credential ref: %w", err)
	}
	plaintext, err := marshalCredential(value)
	if err != nil {
		return biz.CredentialLocator{}, fmt.Errorf("marshal credential: %w", err)
	}
	// AAD binds cluster_id + ref + credential_revision (design §5.5). Built
	// fresh on Put (ref just allocated) and rebuilt on Get from the Locator.
	aad := buildAAD(clusterID, ref, credentialRevision)
	key := s.keys[s.activeVersion]
	block, err := aes.NewCipher(key)
	if err != nil {
		return biz.CredentialLocator{}, fmt.Errorf("init AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return biz.CredentialLocator{}, fmt.Errorf("init GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return biz.CredentialLocator{}, fmt.Errorf("generate nonce: %w", err)
	}
	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)
	row := k8sCredentialModel{
		Ref:                ref,
		ClusterID:          clusterID,
		CredentialRevision: credentialRevision,
		Ciphertext:         ciphertext,
		Nonce:              nonce,
		KeyVersion:         s.activeVersion,
		CredentialType:     string(value.Kind),
	}
	if err := s.db(ctx).Create(&row).Error; err != nil {
		return biz.CredentialLocator{}, fmt.Errorf("persist credential row: %w", err)
	}
	return biz.CredentialLocator{
		ClusterID:          clusterID,
		CredentialRef:      ref,
		CredentialRevision: credentialRevision,
	}, nil
}

// Get reads the DB row, verifies cluster_id/ref/credential_revision match the
// locator (drift → ErrClusterFailedPrecondition), then reconstructs AAD from
// the locator and decrypts. AAD is never read from DB.
func (s *aeadCredentialStore) Get(ctx context.Context, locator biz.CredentialLocator) (kubernetesx.Credential, error) {
	var row k8sCredentialModel
	if err := s.db(ctx).Where("ref = ?", locator.CredentialRef).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return kubernetesx.Credential{}, fmt.Errorf("%w: credential ref not found: %s", biz.ErrClusterFailedPrecondition, locator.CredentialRef)
		}
		return kubernetesx.Credential{}, fmt.Errorf("load credential row: %w", err)
	}
	// Drift check (design §5.5): the DB row must match the Locator on all
	// three AAD-bound fields. A mismatch means the caller passed a stale
	// Locator or the row was rotated out from under them — reject without
	// attempting decryption (the AAD would not match anyway, but failing
	// early gives a clearer error than GCM auth failure).
	if row.ClusterID != locator.ClusterID || row.CredentialRevision != locator.CredentialRevision {
		return kubernetesx.Credential{}, fmt.Errorf("%w: credential row drifted (db cluster=%s rev=%d, locator cluster=%s rev=%d)",
			biz.ErrClusterFailedPrecondition, row.ClusterID, row.CredentialRevision, locator.ClusterID, locator.CredentialRevision)
	}
	key, ok := s.keys[row.KeyVersion]
	if !ok {
		return kubernetesx.Credential{}, fmt.Errorf("%w: master key version %q not loaded (key retired before re-encrypt complete?)",
			biz.ErrClusterFailedPrecondition, row.KeyVersion)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return kubernetesx.Credential{}, fmt.Errorf("init AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return kubernetesx.Credential{}, fmt.Errorf("init GCM: %w", err)
	}
	aad := buildAAD(locator.ClusterID, locator.CredentialRef, locator.CredentialRevision)
	plaintext, err := gcm.Open(nil, row.Nonce, row.Ciphertext, aad)
	if err != nil {
		return kubernetesx.Credential{}, fmt.Errorf("%w: AEAD open failed (AAD mismatch or ciphertext tampered): %w", biz.ErrClusterFailedPrecondition, err)
	}
	cred, err := unmarshalCredential(plaintext, row.CredentialType)
	if err != nil {
		return kubernetesx.Credential{}, fmt.Errorf("unmarshal credential: %w", err)
	}
	return cred, nil
}

// Delete removes a credential row by ref. Idempotent: missing row is not an
// error (rotate cleanup may run twice via outbox retry, design §5.7.3 step 6).
func (s *aeadCredentialStore) Delete(ctx context.Context, ref string) error {
	tx := s.db(ctx).Where("ref = ?", ref).Delete(&k8sCredentialModel{})
	if tx.Error != nil && !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return fmt.Errorf("delete credential row: %w", tx.Error)
	}
	return nil
}

// RotateKey re-encrypts every row whose key_version == fromVersion to
// toVersion: decrypt with old key → re-encrypt with new key + fresh nonce →
// write back ciphertext/nonce/key_version. Returns the count of re-encrypted
// rows. Rows already at toVersion are skipped. Design §5.5: V1 cost is full
// decrypt+reencrypt per row (no DEK rewrap fast path); acceptable because
// credential count is small.
func (s *aeadCredentialStore) RotateKey(ctx context.Context, fromVersion, toVersion string) (int, error) {
	oldKey, ok := s.keys[fromVersion]
	if !ok {
		return 0, fmt.Errorf("rotate key: fromVersion %q not loaded", fromVersion)
	}
	newKey, ok := s.keys[toVersion]
	if !ok {
		return 0, fmt.Errorf("rotate key: toVersion %q not loaded", toVersion)
	}
	var rows []k8sCredentialModel
	if err := s.db(ctx).Where("key_version = ?", fromVersion).Find(&rows).Error; err != nil {
		return 0, fmt.Errorf("load rows for rotation: %w", err)
	}
	oldBlock, err := aes.NewCipher(oldKey)
	if err != nil {
		return 0, fmt.Errorf("init old AES cipher: %w", err)
	}
	oldGCM, err := cipher.NewGCM(oldBlock)
	if err != nil {
		return 0, fmt.Errorf("init old GCM: %w", err)
	}
	newBlock, err := aes.NewCipher(newKey)
	if err != nil {
		return 0, fmt.Errorf("init new AES cipher: %w", err)
	}
	newGCM, err := cipher.NewGCM(newBlock)
	if err != nil {
		return 0, fmt.Errorf("init new GCM: %w", err)
	}
	reencrypted := 0
	for _, row := range rows {
		// AAD is reconstructed from the row's own cluster_id/ref/revision
		// (the Locator the row was originally Put with). Decrypt then
		// re-encrypt under the new key with a fresh nonce.
		aad := buildAAD(row.ClusterID, row.Ref, row.CredentialRevision)
		plaintext, err := oldGCM.Open(nil, row.Nonce, row.Ciphertext, aad)
		if err != nil {
			return reencrypted, fmt.Errorf("rotate key: decrypt row %s failed: %w", row.Ref, err)
		}
		nonce := make([]byte, newGCM.NonceSize())
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			return reencrypted, fmt.Errorf("rotate key: generate nonce: %w", err)
		}
		newCiphertext := newGCM.Seal(nil, nonce, plaintext, aad)
		// Write back ciphertext/nonce/key_version in place. Leave ref,
		// cluster_id, credential_revision untouched (AAD is unchanged, so
		// the Locator stays valid — Get still reconstructs the same AAD).
		if err := s.db(ctx).Model(&k8sCredentialModel{}).
			Where("ref = ?", row.Ref).
			Updates(map[string]any{
				"ciphertext":  newCiphertext,
				"nonce":       nonce,
				"key_version": toVersion,
			}).Error; err != nil {
			return reencrypted, fmt.Errorf("rotate key: write back row %s: %w", row.Ref, err)
		}
		reencrypted++
	}
	return reencrypted, nil
}

// buildAAD constructs the AEAD additional authenticated data for a credential:
// cluster_id + ref + credential_revision (design §5.5). The layout is
// length-prefixed to prevent concatenation ambiguity (cluster_id="ab",
// ref="c", revision=0 would otherwise collide with cluster_id="a", ref="bc",
// revision=0). revision is encoded as a fixed-width 8-byte big-endian int64.
func buildAAD(clusterID, ref string, credentialRevision int64) []byte {
	var buf bytes.Buffer
	writeLenPrefix := func(s string) {
		_ = binary.Write(&buf, binary.BigEndian, uint32(len(s)))
		_, _ = buf.WriteString(s)
	}
	writeLenPrefix(clusterID)
	writeLenPrefix(ref)
	_ = binary.Write(&buf, binary.BigEndian, uint64(credentialRevision))
	return buf.Bytes()
}

// marshalCredential serializes a kubernetesx.Credential for encryption. JSON
// is used because Credential has variable-length fields (Kubeconfig []byte,
// Token string) and the Kind enum — a stable text format is simpler than a
// bespoke binary layout and survives kernel Credential field additions
// gracefully (unknown fields are preserved on unmarshal).
func marshalCredential(c kubernetesx.Credential) ([]byte, error) {
	return json.Marshal(c)
}

// unmarshalCredential reverses marshalCredential. credentialType is the
// original CredentialKind stored on the row; it is re-applied as a
// cross-check (if the marshaled Kind drifted, the row is corrupt).
func unmarshalCredential(plaintext []byte, credentialType string) (kubernetesx.Credential, error) {
	var c kubernetesx.Credential
	if err := json.Unmarshal(plaintext, &c); err != nil {
		return kubernetesx.Credential{}, err
	}
	if string(c.Kind) != credentialType {
		return kubernetesx.Credential{}, fmt.Errorf("credential kind drift: row type %q, plaintext kind %q", credentialType, c.Kind)
	}
	return c, nil
}

// newCredentialRef returns a fresh UUIDv4 string for the credential ref. The
// ref is the primary key of k8s_cluster_credentials and is part of the AAD,
// so uniqueness + unpredictability matter; UUIDv4 provides both.
func newCredentialRef() (string, error) {
	return uuid.NewString(), nil
}
