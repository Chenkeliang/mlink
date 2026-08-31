package identity

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/crypto/scrypt"

	"mlink/internal/config"
)

const bundleFormat = "mlink-identity-bundle/v1"

var ErrBundleAuthentication = errors.New("MLink identity bundle authentication failed")

type BundleV1 struct {
	SchemaVersion int                           `json:"schema_version"`
	Principals    map[string]config.Principal   `json:"principals"`
	Spaces        map[string]config.MemorySpace `json:"spaces"`
	IdentityKey   []byte                        `json:"identity_key"`
	Bindings      []BindingExport               `json:"bindings"`
	CreatedAt     time.Time                     `json:"created_at"`
}

func (bundle BundleV1) String() string {
	return fmt.Sprintf("BundleV1{SchemaVersion:%d Principals:%d Spaces:%d Bindings:%d CreatedAt:%s IdentityKey:<redacted>}", bundle.SchemaVersion, len(bundle.Principals), len(bundle.Spaces), len(bundle.Bindings), bundle.CreatedAt.UTC().Format(time.RFC3339))
}

func (bundle BundleV1) GoString() string { return bundle.String() }

func (bundle *BundleV1) Wipe() {
	if bundle == nil {
		return
	}
	wipeDigest(bundle.IdentityKey)
	bundle.IdentityKey = nil
	for index := range bundle.Bindings {
		bundle.Bindings[index].Wipe()
	}
}

type BindingExport struct {
	Ref   config.BindingRef `json:"ref"`
	Value []byte            `json:"value"`
}

func (binding BindingExport) String() string {
	return fmt.Sprintf("BindingExport{SlotID:%q Source:%q Kind:%q PrincipalID:%q Status:%q Value:<redacted>}", binding.Ref.ID, binding.Ref.Source, binding.Ref.Kind, binding.Ref.PrincipalID, binding.Ref.Status)
}

func (binding BindingExport) GoString() string { return binding.String() }

func (binding *BindingExport) Wipe() {
	if binding == nil {
		return
	}
	wipeDigest(binding.Value)
	binding.Value = nil
}

type encryptedEnvelope struct {
	Format     string `json:"format"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

func EncryptBundle(bundle BundleV1, passphrase []byte, random io.Reader) ([]byte, error) {
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	if len(passphrase) < 12 {
		return nil, errors.New("identity bundle passphrase must contain at least 12 bytes")
	}
	if random == nil {
		return nil, errors.New("identity bundle randomness is required")
	}
	salt := make([]byte, 16)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, fmt.Errorf("read identity bundle salt: %w", err)
	}
	key, err := scrypt.Key(passphrase, salt, 32768, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("derive identity bundle key: %w", err)
	}
	defer wipeDigest(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return nil, fmt.Errorf("read identity bundle nonce: %w", err)
	}
	plaintext, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	defer wipeDigest(plaintext)
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(bundleFormat))
	envelope := encryptedEnvelope{
		Format: bundleFormat, Salt: base64.RawStdEncoding.EncodeToString(salt),
		Nonce: base64.RawStdEncoding.EncodeToString(nonce), Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func DecryptBundle(encrypted, passphrase []byte) (BundleV1, error) {
	if len(passphrase) < 12 {
		return BundleV1{}, ErrBundleAuthentication
	}
	decoder := json.NewDecoder(bytes.NewReader(encrypted))
	decoder.DisallowUnknownFields()
	var envelope encryptedEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return BundleV1{}, fmt.Errorf("decode identity bundle envelope: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BundleV1{}, errors.New("identity bundle contains multiple JSON values")
	}
	if envelope.Format != bundleFormat {
		return BundleV1{}, errors.New("unsupported identity bundle format")
	}
	salt, err := base64.RawStdEncoding.DecodeString(envelope.Salt)
	if err != nil || len(salt) != 16 {
		return BundleV1{}, errors.New("invalid identity bundle salt")
	}
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil {
		return BundleV1{}, errors.New("invalid identity bundle nonce")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil {
		return BundleV1{}, errors.New("invalid identity bundle ciphertext")
	}
	key, err := scrypt.Key(passphrase, salt, 32768, 8, 1, 32)
	if err != nil {
		return BundleV1{}, ErrBundleAuthentication
	}
	defer wipeDigest(key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return BundleV1{}, ErrBundleAuthentication
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(nonce) != gcm.NonceSize() {
		return BundleV1{}, ErrBundleAuthentication
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, []byte(bundleFormat))
	if err != nil {
		return BundleV1{}, ErrBundleAuthentication
	}
	defer wipeDigest(plaintext)
	plainDecoder := json.NewDecoder(bytes.NewReader(plaintext))
	plainDecoder.DisallowUnknownFields()
	var bundle BundleV1
	if err := plainDecoder.Decode(&bundle); err != nil {
		return BundleV1{}, fmt.Errorf("decode identity bundle: %w", err)
	}
	if err := plainDecoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		bundle.Wipe()
		return BundleV1{}, errors.New("identity bundle plaintext contains multiple JSON values")
	}
	if err := validateBundle(bundle); err != nil {
		bundle.Wipe()
		return BundleV1{}, err
	}
	return bundle, nil
}

func WriteBundleAtomic(path string, encrypted []byte) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean == string(filepath.Separator) || len(encrypted) == 0 {
		return errors.New("absolute non-root identity bundle path and content are required")
	}
	directory := filepath.Dir(clean)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".mlink-identity-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encrypted); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, clean); err != nil {
		return err
	}
	committed = true
	if err := os.Chmod(clean, 0o600); err != nil {
		return err
	}
	directoryFile, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

func validateBundle(bundle BundleV1) error {
	if bundle.SchemaVersion != 1 || len(bundle.IdentityKey) != 32 || len(bundle.Principals) == 0 || len(bundle.Spaces) == 0 || bundle.CreatedAt.IsZero() {
		return errors.New("identity bundle fields are invalid")
	}
	for id, principal := range bundle.Principals {
		if id != principal.ID || principal.ID == "" || principal.CanonicalUserID == "" || principal.Kind != config.PrincipalPerson {
			return errors.New("identity bundle Principal is invalid")
		}
	}
	for id, space := range bundle.Spaces {
		if id != space.ID || space.ID == "" || space.ConnectionID == "" || space.TenantID == "" || space.AgentID == "" {
			return errors.New("identity bundle Memory Space is invalid")
		}
		if space.PrincipalPolicy == config.PolicyFixed {
			if _, exists := bundle.Principals[space.PrincipalID]; !exists {
				return errors.New("identity bundle fixed Space Principal is missing")
			}
		} else if space.PrincipalPolicy != config.PolicyExternalHMAC && space.PrincipalPolicy != config.PolicyGroupHMAC {
			return errors.New("identity bundle Space policy is invalid")
		}
	}
	seen := make(map[string]bool, len(bundle.Bindings))
	for _, binding := range bundle.Bindings {
		if seen[binding.Ref.ID] || len(binding.Value) == 0 || bytes.ContainsAny(binding.Value, "\r\n") {
			return errors.New("identity bundle Binding is invalid")
		}
		seen[binding.Ref.ID] = true
		if err := config.ValidateBindingRef(binding.Ref); err != nil {
			return err
		}
		if _, exists := bundle.Principals[binding.Ref.PrincipalID]; !exists {
			return errors.New("identity bundle Binding Principal is missing")
		}
	}
	return nil
}

func ValidateBundle(bundle BundleV1) error { return validateBundle(bundle) }
