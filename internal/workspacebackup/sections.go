package workspacebackup

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maximumManifestBytes  = 4 << 20
	maximumChecksumsBytes = 1 << 20
)

var (
	manifestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

type SectionSource struct {
	Name Section
	Open func(context.Context) (io.ReadCloser, error)
}

type StagedSection struct {
	Name         Section
	Path         string
	CipherBytes  int64
	CipherSHA256 string
}

type Packer struct {
	StagingParent string
}

func (packer Packer) Open(ctx context.Context, input string, passphrase []byte, visitor func(Section, io.Reader) error) (Manifest, error) {
	return Open(ctx, input, passphrase, visitor)
}

func EncryptSection(ctx context.Context, staging string, passphrase []byte, source SectionSource) (result StagedSection, err error) {
	if ctx == nil || !source.Name.Valid() || source.Open == nil || !filepath.IsAbs(staging) {
		return StagedSection{}, errors.New("valid workspace backup section source is required")
	}
	info, err := os.Lstat(staging)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return StagedSection{}, errors.New("private workspace backup staging directory is required")
	}
	clear, err := source.Open(ctx)
	if err != nil {
		return StagedSection{}, fmt.Errorf("open encrypted section source %q", source.Name)
	}
	defer clear.Close()
	temporary, err := os.CreateTemp(staging, string(source.Name)+"-*.agepart")
	if err != nil {
		return StagedSection{}, errors.New("create encrypted section staging file")
	}
	path := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(path)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return StagedSection{}, errors.New("protect encrypted section staging file")
	}
	if err := Encrypt(temporary, passphrase, clear); err != nil {
		return StagedSection{}, err
	}
	if err := temporary.Sync(); err != nil {
		return StagedSection{}, errors.New("sync encrypted section staging file")
	}
	if err := temporary.Close(); err != nil {
		return StagedSection{}, errors.New("close encrypted section staging file")
	}
	file, err := os.Open(path)
	if err != nil {
		return StagedSection{}, errors.New("open encrypted section for hashing")
	}
	digest := sha256.New()
	size, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return StagedSection{}, errors.New("hash encrypted workspace section")
	}
	committed = true
	return StagedSection{Name: source.Name, Path: path, CipherBytes: size, CipherSHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func (packer Packer) Pack(ctx context.Context, output string, passphrase []byte, manifest *Manifest, sources ...SectionSource) error {
	if ctx == nil || !filepath.IsAbs(output) || !filepath.IsAbs(packer.StagingParent) {
		return errors.New("absolute workspace backup output and staging paths are required")
	}
	if manifest == nil {
		return ErrInvalidBundle
	}
	if err := VerifyManifest(*manifest); err != nil {
		return err
	}
	if err := validateSectionSources(sources); err != nil {
		return err
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("workspace backup output already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return errors.New("inspect workspace backup output")
	}
	parent := filepath.Dir(output)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&fs.ModeSymlink != 0 {
		return errors.New("safe workspace backup output directory is required")
	}
	stagingInfo, err := os.Lstat(packer.StagingParent)
	if err != nil || !stagingInfo.IsDir() || stagingInfo.Mode()&fs.ModeSymlink != 0 {
		return errors.New("safe workspace backup staging parent is required")
	}
	staging, err := os.MkdirTemp(packer.StagingParent, ".mlink-backup-*")
	if err != nil {
		return errors.New("create workspace backup staging directory")
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return errors.New("protect workspace backup staging directory")
	}

	staged := make([]StagedSection, 0, len(sources)+2)
	for _, source := range sources {
		section, err := EncryptSection(ctx, staging, passphrase, source)
		if err != nil {
			return err
		}
		staged = append(staged, section)
	}
	manifest.Sections = make([]SectionManifest, 0, len(staged))
	for _, section := range staged {
		manifest.Sections = append(manifest.Sections, SectionManifest{Name: section.Name, CipherBytes: section.CipherBytes, CipherSHA256: section.CipherSHA256})
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		return errors.New("encode workspace backup manifest")
	}
	manifestSection, err := EncryptSection(ctx, staging, passphrase, memorySection(SectionManifestData, manifestData))
	if err != nil {
		return err
	}
	checksumsSection, err := EncryptSection(ctx, staging, passphrase, memorySection(SectionChecksums, encodeChecksums(manifest.Sections)))
	if err != nil {
		return err
	}
	ordered := append([]StagedSection{manifestSection, checksumsSection}, staged...)

	temporary, err := os.CreateTemp(parent, ".mlink-backup-*")
	if err != nil {
		return errors.New("create atomic workspace backup output")
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errors.New("protect workspace backup output")
	}
	reader, writer := io.Pipe()
	tarErrors := make(chan error, 1)
	go func() {
		tarErrors <- writeOuterTar(writer, ordered)
	}()
	encryptErr := Encrypt(temporary, passphrase, reader)
	if encryptErr != nil {
		_ = reader.CloseWithError(encryptErr)
	}
	tarErr := <-tarErrors
	if encryptErr != nil {
		return encryptErr
	}
	if tarErr != nil {
		return tarErr
	}
	if err := temporary.Sync(); err != nil {
		return errors.New("sync workspace backup output")
	}
	if err := temporary.Close(); err != nil {
		return errors.New("close workspace backup output")
	}
	if err := os.Link(temporaryPath, output); err != nil {
		return errors.New("commit workspace backup output")
	}
	if err := os.Remove(temporaryPath); err != nil {
		_ = os.Remove(output)
		return errors.New("finalize workspace backup output")
	}
	directory, err := os.Open(parent)
	if err != nil {
		return errors.New("open workspace backup output directory")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync workspace backup output directory")
	}
	return nil
}

func Open(ctx context.Context, input string, passphrase []byte, visitor func(Section, io.Reader) error) (Manifest, error) {
	if ctx == nil || !filepath.IsAbs(input) || visitor == nil {
		return Manifest{}, errors.New("valid workspace backup input and visitor are required")
	}
	info, err := os.Lstat(input)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 || info.Size() <= 0 {
		return Manifest{}, ErrInvalidBundle
	}
	file, err := os.Open(input)
	if err != nil {
		return Manifest{}, ErrInvalidBundle
	}
	defer file.Close()
	outer, err := decryptReader(passphrase, file)
	if err != nil {
		return Manifest{}, err
	}
	tarReader := tar.NewReader(outer)
	var manifest Manifest
	expected := map[Section]SectionManifest{}
	visited := map[Section]bool{}
	manifestSeen, checksumsSeen := false, false
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, ErrAuthentication
		}
		section, ok := sectionFromEntry(header.Name)
		if !ok || header.Size <= 0 || visited[section] {
			return Manifest{}, ErrInvalidBundle
		}
		visited[section] = true
		limited := io.LimitReader(tarReader, header.Size)
		digest := sha256.New()
		ciphertext := io.TeeReader(limited, digest)
		clear, err := decryptReader(passphrase, ciphertext)
		if err != nil {
			return Manifest{}, err
		}
		switch section {
		case SectionManifestData:
			data, err := io.ReadAll(io.LimitReader(clear, maximumManifestBytes+1))
			if err != nil || len(data) > maximumManifestBytes {
				return Manifest{}, ErrInvalidBundle
			}
			decoder := json.NewDecoder(bytesReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF || VerifyManifest(manifest) != nil {
				return Manifest{}, ErrInvalidBundle
			}
			for _, item := range manifest.Sections {
				if _, exists := expected[item.Name]; exists {
					return Manifest{}, ErrInvalidBundle
				}
				expected[item.Name] = item
			}
			manifestSeen = true
		case SectionChecksums:
			data, err := io.ReadAll(io.LimitReader(clear, maximumChecksumsBytes+1))
			if err != nil || len(data) > maximumChecksumsBytes || !manifestSeen || !checksumsMatch(data, manifest.Sections) {
				return Manifest{}, ErrInvalidBundle
			}
			checksumsSeen = true
		default:
			_, exists := expected[section]
			if !manifestSeen || !checksumsSeen || !exists {
				return Manifest{}, ErrInvalidBundle
			}
			if err := visitor(section, clear); err != nil {
				return Manifest{}, err
			}
		}
		if _, err := io.Copy(io.Discard, clear); err != nil {
			return Manifest{}, ErrAuthentication
		}
		if _, err := io.Copy(io.Discard, limited); err != nil {
			return Manifest{}, ErrAuthentication
		}
		if item, exists := expected[section]; exists {
			if item.CipherBytes != header.Size || item.CipherSHA256 != hex.EncodeToString(digest.Sum(nil)) {
				return Manifest{}, ErrAuthentication
			}
		}
	}
	if !manifestSeen || !checksumsSeen {
		return Manifest{}, ErrInvalidBundle
	}
	for section := range expected {
		if !visited[section] {
			return Manifest{}, ErrInvalidBundle
		}
	}
	return manifest, nil
}

func VerifyManifest(manifest Manifest) error {
	if manifest.Format != FormatV1 || manifest.CreatedAt.IsZero() || manifest.MLink.Version == "" ||
		manifest.Provider.ProviderID == "" || manifest.Provider.DriverVersion == "" || !safeManifestID(manifest.Provider.InstanceID) ||
		!digestPattern.MatchString(manifest.Provider.CoreImageDigest) || !digestPattern.MatchString(manifest.Provider.HubImageDigest) {
		return ErrInvalidBundle
	}
	control := manifest.ControlPlane
	for _, value := range []string{control.InstallationID, control.InstanceID, control.OwnerUserID, control.OwnerTeamID, control.OwnerAgentID, control.OwnerAssetID} {
		if !safeManifestID(value) {
			return ErrInvalidBundle
		}
	}
	if control.InstanceID != manifest.Provider.InstanceID || control.DynamicAgentLimit <= 0 || control.DynamicAgentLimit > 10_000 {
		return ErrInvalidBundle
	}
	for _, volume := range manifest.Provider.Volumes {
		if !volume.Kind.Valid() || !safeManifestID(volume.Name) || volume.LogicalBytes < 0 || volume.FileCount < 0 || volume.SHA256 != "" && len(volume.SHA256) != 64 {
			return ErrInvalidBundle
		}
	}
	for _, item := range manifest.Sections {
		if !item.Name.Valid() || item.Name == SectionManifestData || item.Name == SectionChecksums || item.CipherBytes <= 0 || len(item.CipherSHA256) != 64 {
			return ErrInvalidBundle
		}
	}
	return nil
}

func validateSectionSources(sources []SectionSource) error {
	seen := map[Section]bool{}
	for _, source := range sources {
		if !source.Name.Valid() || source.Name == SectionManifestData || source.Name == SectionChecksums || source.Open == nil || seen[source.Name] {
			return errors.New("unique valid workspace backup section sources are required")
		}
		seen[source.Name] = true
	}
	for _, required := range []Section{SectionCore, SectionMLink, SectionIdentity, SectionSecrets, SectionAgents} {
		if !seen[required] {
			return fmt.Errorf("required workspace backup section %q is missing", required)
		}
	}
	return nil
}

func memorySection(name Section, content []byte) SectionSource {
	return SectionSource{Name: name, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(content)), nil
	}}
}

func writeOuterTar(destination *io.PipeWriter, sections []StagedSection) error {
	archive := tar.NewWriter(destination)
	for _, section := range sections {
		file, err := os.Open(section.Path)
		if err != nil {
			_ = destination.CloseWithError(ErrInvalidBundle)
			return ErrInvalidBundle
		}
		header := &tar.Header{Name: string(section.Name) + ".agepart", Mode: 0o600, Size: section.CipherBytes}
		if err := archive.WriteHeader(header); err != nil {
			_ = file.Close()
			_ = destination.CloseWithError(err)
			return err
		}
		written, err := io.Copy(archive, file)
		closeErr := file.Close()
		if err != nil || closeErr != nil || written != section.CipherBytes {
			_ = destination.CloseWithError(ErrInvalidBundle)
			return ErrInvalidBundle
		}
	}
	if err := archive.Close(); err != nil {
		_ = destination.CloseWithError(err)
		return err
	}
	return destination.Close()
}

func encodeChecksums(sections []SectionManifest) []byte {
	values := append([]SectionManifest(nil), sections...)
	sort.Slice(values, func(left, right int) bool { return values[left].Name < values[right].Name })
	var output strings.Builder
	for _, section := range values {
		fmt.Fprintf(&output, "%s  %s\n", section.CipherSHA256, section.Name)
	}
	return []byte(output.String())
}

func checksumsMatch(content []byte, sections []SectionManifest) bool {
	want := string(encodeChecksums(sections))
	if string(content) != want {
		return false
	}
	scanner := bufio.NewScanner(strings.NewReader(want))
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) != 2 || len(parts[0]) != 64 || !Section(parts[1]).Valid() {
			return false
		}
	}
	return scanner.Err() == nil
}

func sectionFromEntry(name string) (Section, bool) {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".agepart") {
		return "", false
	}
	section := Section(strings.TrimSuffix(name, ".agepart"))
	return section, section.Valid()
}

func safeManifestID(value string) bool { return manifestIDPattern.MatchString(value) }

func bytesReader(value []byte) io.Reader { return bytes.NewReader(value) }
