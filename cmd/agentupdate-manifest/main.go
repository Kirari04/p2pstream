// Command agentupdate-manifest creates, independently signs, merges, and
// verifies bounded canonical agent update metadata. Private keys are read only
// from an environment variable so they do not appear in process arguments.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
	"p2pstream/internal/agentupdate"
)

const maxSigningKeyEnvironmentBytes = 64 << 10

type artifactFlags []string

func (values *artifactFlags) String() string { return strings.Join(*values, ",") }
func (values *artifactFlags) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agentupdate-manifest:", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("expected create, sign, sign-partial, merge, or verify-release subcommand")
	}
	switch arguments[0] {
	case "create":
		return runCreate(arguments[1:])
	case "sign":
		return runSign(arguments[1:], false)
	case "sign-partial":
		return runSign(arguments[1:], true)
	case "merge":
		return runMerge(arguments[1:])
	case "verify-release":
		return runVerifyRelease(arguments[1:])
	default:
		return fmt.Errorf("unknown subcommand %q", arguments[0])
	}
}

func runVerifyRelease(arguments []string) error {
	flags := flag.NewFlagSet("verify-release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var artifacts artifactFlags
	var ociIndexes artifactFlags
	var releaseAssets artifactFlags
	manifestPath := flags.String("manifest", "", "canonical manifest path")
	rootPath := flags.String("root", "", "canonical trusted root path")
	signaturesPath := flags.String("signatures", "", "canonical threshold signature envelope path")
	expectedVersion := flags.String("expected-version", "", "exact expected release version")
	expectedCommit := flags.String("expected-commit", "", "exact expected release commit")
	expectedSequence := flags.Uint64("expected-sequence", 0, "exact expected release sequence")
	expectedChannel := flags.String("expected-channel", "stable", "exact expected release channel")
	serverVersion := flags.String("server-version", "", "management server version used for compatibility verification")
	protocolVersion := flags.Uint("protocol-version", 0, "tunnel protocol used for compatibility verification")
	verificationTime := flags.String("now", "", "verification time as RFC3339; defaults to current UTC time")
	flags.Var(&artifacts, "artifact", "GOOS/GOARCH=raw-binary-path (repeatable; must cover the manifest exactly)")
	flags.Var(&ociIndexes, "oci-index", "repository=raw-OCI-index-path (repeatable; must cover signed OCI images exactly)")
	flags.Var(&releaseAssets, "release-asset", "stable release attachment path (repeatable; must cover signed release assets exactly)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *manifestPath == "" || *rootPath == "" || *signaturesPath == "" ||
		*expectedVersion == "" || *expectedCommit == "" || *expectedSequence == 0 || *serverVersion == "" ||
		*protocolVersion == 0 || *protocolVersion > uint(^uint32(0)) || len(artifacts) == 0 {
		return errors.New("verify-release requires manifest, root, signatures, expected version/commit/sequence, server/protocol versions, artifacts, and no positional arguments")
	}
	now := time.Now().UTC()
	if *verificationTime != "" {
		parsed, err := time.Parse(time.RFC3339, *verificationTime)
		if err != nil || parsed.UTC().Format(time.RFC3339) != *verificationTime {
			return errors.New("verify-release --now must be canonical UTC RFC3339")
		}
		now = parsed
	}
	manifestJSON, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	rootJSON, err := os.ReadFile(*rootPath)
	if err != nil {
		return fmt.Errorf("read root: %w", err)
	}
	signatureJSON, err := os.ReadFile(*signaturesPath)
	if err != nil {
		return fmt.Errorf("read signatures: %w", err)
	}
	root, err := agentupdate.ParseRoot(rootJSON)
	if err != nil {
		return err
	}
	verified, err := agentupdate.VerifyCatalog(manifestJSON, signatureJSON, root, agentupdate.CatalogVerifyPolicy{
		Now:             now,
		RequiredChannel: *expectedChannel,
		ServerVersion:   *serverVersion,
		ProtocolVersion: uint32(*protocolVersion),
	})
	if err != nil {
		return fmt.Errorf("verify signed catalog: %w", err)
	}
	if verified.Manifest.Version != *expectedVersion || verified.Manifest.Commit != *expectedCommit || verified.Manifest.Sequence != *expectedSequence {
		return errors.New("signed manifest does not match the expected release identity")
	}
	if len(artifacts) != len(verified.Manifest.Artifacts) {
		return fmt.Errorf("artifact set has %d entries; signed manifest requires %d", len(artifacts), len(verified.Manifest.Artifacts))
	}
	seen := make(map[string]struct{}, len(artifacts))
	for _, specification := range artifacts {
		identity, path, ok := strings.Cut(specification, "=")
		if !ok || identity == "" || path == "" {
			return fmt.Errorf("invalid artifact specification %q", specification)
		}
		goos, goarch, ok := strings.Cut(identity, "/")
		if !ok || strings.Contains(goarch, "/") {
			return fmt.Errorf("invalid artifact platform %q", identity)
		}
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("duplicate artifact platform %q", identity)
		}
		seen[identity] = struct{}{}
		expected, err := verified.Manifest.ArtifactFor(goos, goarch)
		if err != nil {
			return err
		}
		if filepath.Base(path) != expected.Name {
			return fmt.Errorf("artifact %s filename does not match signed name %s", identity, expected.Name)
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open artifact %s: %w", identity, err)
		}
		verifyErr := agentupdate.VerifyArtifact(file, expected)
		closeErr := file.Close()
		if verifyErr != nil {
			return fmt.Errorf("verify artifact %s: %w", identity, verifyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close artifact %s: %w", identity, closeErr)
		}
	}
	if len(ociIndexes) != len(verified.Manifest.OCIImages) {
		return fmt.Errorf("OCI index set has %d entries; signed manifest requires %d", len(ociIndexes), len(verified.Manifest.OCIImages))
	}
	seenRepositories := make(map[string]struct{}, len(ociIndexes))
	for _, specification := range ociIndexes {
		repository, path, ok := strings.Cut(specification, "=")
		if !ok || repository == "" || path == "" {
			return fmt.Errorf("invalid OCI index specification %q", specification)
		}
		if _, duplicate := seenRepositories[repository]; duplicate {
			return fmt.Errorf("duplicate OCI image repository %q", repository)
		}
		seenRepositories[repository] = struct{}{}
		expected, err := verified.Manifest.OCIImageFor(repository)
		if err != nil {
			return err
		}
		raw, err := readBoundedRegularFile(path, agentupdate.MaxManifestBytes)
		if err != nil {
			return fmt.Errorf("read OCI image index %q: %w", repository, err)
		}
		if err := agentupdate.VerifyOCIImageIndex(raw, expected); err != nil {
			return fmt.Errorf("verify OCI image index %q: %w", repository, err)
		}
	}
	if len(releaseAssets) != len(verified.Manifest.ReleaseAssets) {
		return fmt.Errorf("release asset set has %d entries; signed manifest requires %d", len(releaseAssets), len(verified.Manifest.ReleaseAssets))
	}
	seenReleaseAssets := make(map[string]struct{}, len(releaseAssets))
	for _, path := range releaseAssets {
		name := filepath.Base(path)
		if _, duplicate := seenReleaseAssets[name]; duplicate {
			return fmt.Errorf("duplicate release asset name %q", name)
		}
		seenReleaseAssets[name] = struct{}{}
		expected, err := verified.Manifest.ReleaseAssetFor(name)
		if err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open release asset %q: %w", name, err)
		}
		verifyErr := agentupdate.VerifyReleaseAsset(file, expected)
		closeErr := file.Close()
		if verifyErr != nil {
			return fmt.Errorf("verify release asset %q: %w", name, verifyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close release asset %q: %w", name, closeErr)
		}
	}
	return nil
}

func runCreate(arguments []string) error {
	flags := flag.NewFlagSet("create", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var artifacts artifactFlags
	var ociIndexes artifactFlags
	var releaseAssets artifactFlags
	channel := flags.String("channel", "stable", "manifest channel")
	rootPath := flags.String("root", "", "canonical trusted root path")
	version := flags.String("version", "", "release version")
	commit := flags.String("commit", "", "release commit")
	sequence := flags.Uint64("sequence", 0, "monotonic release sequence")
	publishedAt := flags.String("published-at", "", "publication time")
	expiresAt := flags.String("expires-at", "", "expiry time")
	minimumSafeVersion := flags.String("minimum-safe-version", "", "minimum safe release")
	securityEpoch := flags.Uint64("security-epoch", 0, "security epoch")
	serverMin := flags.String("server-min", "", "minimum compatible server")
	serverMax := flags.String("server-max", "", "maximum compatible server")
	protocolMin := flags.Uint("protocol-min", 0, "minimum compatible protocol")
	protocolMax := flags.Uint("protocol-max", 0, "maximum compatible protocol")
	updaterMin := flags.String("updater-min", "", "minimum compatible updater")
	updaterMax := flags.String("updater-max", "", "maximum compatible updater")
	output := flags.String("output", "", "exclusive output path")
	flags.Var(&artifacts, "artifact", "GOOS/GOARCH=raw-binary-path (repeatable)")
	flags.Var(&ociIndexes, "oci-index", "repository=raw-OCI-index-path (repeatable)")
	flags.Var(&releaseAssets, "release-asset", "stable release attachment path (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *output == "" || *rootPath == "" {
		return errors.New("create requires --root, --output, and no positional arguments")
	}
	if *protocolMin > uint(^uint32(0)) || *protocolMax > uint(^uint32(0)) {
		return errors.New("protocol version exceeds uint32")
	}
	rootJSON, err := os.ReadFile(*rootPath)
	if err != nil {
		return fmt.Errorf("read root: %w", err)
	}
	root, err := agentupdate.ParseRoot(rootJSON)
	if err != nil {
		return err
	}
	manifestArtifacts := make([]agentupdate.Artifact, 0, len(artifacts))
	for _, specification := range artifacts {
		artifact, err := inspectArtifact(specification)
		if err != nil {
			return err
		}
		manifestArtifacts = append(manifestArtifacts, artifact)
	}
	slices.SortFunc(manifestArtifacts, func(a, b agentupdate.Artifact) int {
		return strings.Compare(a.OS+"/"+a.Arch, b.OS+"/"+b.Arch)
	})
	manifestOCIImages := make([]agentupdate.OCIImage, 0, len(ociIndexes))
	for _, specification := range ociIndexes {
		image, err := inspectOCIIndex(specification)
		if err != nil {
			return err
		}
		manifestOCIImages = append(manifestOCIImages, image)
	}
	slices.SortFunc(manifestOCIImages, func(a, b agentupdate.OCIImage) int {
		return strings.Compare(a.Repository, b.Repository)
	})
	manifestReleaseAssets := make([]agentupdate.ReleaseAsset, 0, len(releaseAssets))
	for _, path := range releaseAssets {
		asset, err := inspectReleaseAsset(path)
		if err != nil {
			return err
		}
		manifestReleaseAssets = append(manifestReleaseAssets, asset)
	}
	slices.SortFunc(manifestReleaseAssets, func(a, b agentupdate.ReleaseAsset) int {
		return strings.Compare(a.Name, b.Name)
	})
	manifest := agentupdate.Manifest{
		SchemaVersion:      agentupdate.SchemaVersion,
		Channel:            *channel,
		RootVersion:        root.Version,
		Version:            *version,
		Commit:             *commit,
		Sequence:           *sequence,
		PublishedAt:        *publishedAt,
		ExpiresAt:          *expiresAt,
		MinimumSafeVersion: *minimumSafeVersion,
		SecurityEpoch:      *securityEpoch,
		Compatibility: agentupdate.Compatibility{
			Server:   agentupdate.VersionRange{Min: *serverMin, Max: *serverMax},
			Protocol: agentupdate.ProtocolRange{Min: uint32(*protocolMin), Max: uint32(*protocolMax)},
			Updater:  agentupdate.VersionRange{Min: *updaterMin, Max: *updaterMax},
		},
		Artifacts:     manifestArtifacts,
		OCIImages:     manifestOCIImages,
		ReleaseAssets: manifestReleaseAssets,
	}
	data, err := agentupdate.CanonicalManifest(manifest)
	if err != nil {
		return fmt.Errorf("create manifest: %w", err)
	}
	return writeExclusive(*output, data, 0o644)
}

func inspectReleaseAsset(path string) (agentupdate.ReleaseAsset, error) {
	file, err := os.Open(path)
	if err != nil {
		return agentupdate.ReleaseAsset{}, fmt.Errorf("open release asset %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return agentupdate.ReleaseAsset{}, fmt.Errorf("stat release asset %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > agentupdate.MaxArtifactSize {
		return agentupdate.ReleaseAsset{}, fmt.Errorf("release asset %q must be a regular file of 1..%d bytes", path, agentupdate.MaxArtifactSize)
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, file)
	if err != nil {
		return agentupdate.ReleaseAsset{}, fmt.Errorf("hash release asset %q: %w", path, err)
	}
	if written != info.Size() {
		return agentupdate.ReleaseAsset{}, fmt.Errorf("release asset %q changed while hashing", path)
	}
	return agentupdate.ReleaseAsset{
		Name: filepath.Base(path), Size: uint64(info.Size()), SHA256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func inspectOCIIndex(specification string) (agentupdate.OCIImage, error) {
	repository, path, ok := strings.Cut(specification, "=")
	if !ok || repository == "" || path == "" {
		return agentupdate.OCIImage{}, fmt.Errorf("invalid OCI index specification %q", specification)
	}
	raw, err := readBoundedRegularFile(path, agentupdate.MaxManifestBytes)
	if err != nil {
		return agentupdate.OCIImage{}, fmt.Errorf("read OCI image index %q: %w", path, err)
	}
	image, err := agentupdate.ParseOCIImageIndex(repository, raw)
	if err != nil {
		return agentupdate.OCIImage{}, fmt.Errorf("inspect OCI image index %q: %w", path, err)
	}
	return image, nil
}

func readBoundedRegularFile(path string, maximum int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(maximum) {
		return nil, fmt.Errorf("file must be regular and 1..%d bytes", maximum)
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) != int(info.Size()) {
		return nil, errors.New("file changed while reading")
	}
	return data, nil
}

func runSign(arguments []string, partial bool) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	manifestPath := flags.String("manifest", "", "canonical manifest path")
	rootPath := flags.String("root", "", "canonical trusted root path")
	signaturesOutput := flags.String("signatures-output", "", "exclusive signatures output path")
	keyFile := flags.String("key-file", "", "0600 file containing canonical base64 signing key(s)")
	keyStdin := flags.Bool("key-stdin", false, "read canonical base64 signing key(s) from standard input")
	keysEnvironment := flags.String("keys-env", "", "environment variable containing base64 keys (discouraged; compatibility only)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *manifestPath == "" || *rootPath == "" || *signaturesOutput == "" {
		return errors.New("sign requires --manifest, --root, --signatures-output, and no positional arguments")
	}
	sources := 0
	if *keyFile != "" {
		sources++
	}
	if *keyStdin {
		sources++
	}
	if *keysEnvironment != "" {
		sources++
	}
	if sources != 1 {
		return errors.New("sign requires exactly one of --key-file, --key-stdin, or --keys-env")
	}
	manifestJSON, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	rootJSON, err := os.ReadFile(*rootPath)
	if err != nil {
		return fmt.Errorf("read root: %w", err)
	}
	root, err := agentupdate.ParseRoot(rootJSON)
	if err != nil {
		return err
	}
	var privateKeys []ed25519.PrivateKey
	switch {
	case *keyFile != "":
		privateKeys, err = privateKeysFromFile(*keyFile)
	case *keyStdin:
		privateKeys, err = privateKeysFromReader(os.Stdin)
	default:
		privateKeys, err = privateKeysFromEnvironment(*keysEnvironment)
	}
	if err != nil {
		return err
	}
	var envelope agentupdate.SignatureEnvelope
	if partial {
		envelope, err = agentupdate.SignManifestPartial(manifestJSON, root, privateKeys)
	} else {
		envelope, err = agentupdate.SignManifest(manifestJSON, root, privateKeys)
	}
	if err != nil {
		return fmt.Errorf("sign manifest: %w", err)
	}
	signaturesJSON, err := agentupdate.CanonicalSignatures(envelope)
	if err != nil {
		return err
	}
	if err := writeExclusive(*signaturesOutput, signaturesJSON, 0o644); err != nil {
		return err
	}
	return nil
}

func runMerge(arguments []string) error {
	flags := flag.NewFlagSet("merge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var signaturePaths artifactFlags
	manifestPath := flags.String("manifest", "", "canonical manifest path")
	rootPath := flags.String("root", "", "canonical trusted root path")
	output := flags.String("output", "", "exclusive merged signature output path")
	flags.Var(&signaturePaths, "signature", "partial canonical signature envelope path (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *manifestPath == "" || *rootPath == "" || *output == "" || len(signaturePaths) == 0 {
		return errors.New("merge requires --manifest, --root, --output, one or more --signature values, and no positional arguments")
	}
	manifestJSON, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}
	rootJSON, err := os.ReadFile(*rootPath)
	if err != nil {
		return fmt.Errorf("read root: %w", err)
	}
	root, err := agentupdate.ParseRoot(rootJSON)
	if err != nil {
		return err
	}
	contributions := make([]agentupdate.SignatureEnvelope, 0, len(signaturePaths))
	for _, path := range signaturePaths {
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read signature contribution %q: %w", path, err)
		}
		envelope, err := agentupdate.ParseSignatures(data)
		if err != nil {
			return fmt.Errorf("parse signature contribution %q: %w", path, err)
		}
		contributions = append(contributions, envelope)
	}
	merged, err := agentupdate.MergeManifestSignatures(manifestJSON, root, contributions...)
	if err != nil {
		return fmt.Errorf("merge manifest signatures: %w", err)
	}
	data, err := agentupdate.CanonicalSignatures(merged)
	if err != nil {
		return err
	}
	return writeExclusive(*output, data, 0o644)
}

func inspectArtifact(specification string) (agentupdate.Artifact, error) {
	identity, path, ok := strings.Cut(specification, "=")
	if !ok || identity == "" || path == "" {
		return agentupdate.Artifact{}, fmt.Errorf("invalid artifact specification %q", specification)
	}
	goos, goarch, ok := strings.Cut(identity, "/")
	if !ok || strings.Contains(goarch, "/") {
		return agentupdate.Artifact{}, fmt.Errorf("invalid artifact platform %q", identity)
	}
	file, err := os.Open(path)
	if err != nil {
		return agentupdate.Artifact{}, fmt.Errorf("open artifact %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return agentupdate.Artifact{}, fmt.Errorf("stat artifact %q: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > agentupdate.MaxArtifactSize {
		return agentupdate.Artifact{}, fmt.Errorf("artifact %q must be a regular file of 1..%d bytes", path, agentupdate.MaxArtifactSize)
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, file)
	if err != nil {
		return agentupdate.Artifact{}, fmt.Errorf("hash artifact %q: %w", path, err)
	}
	if written != info.Size() {
		return agentupdate.Artifact{}, fmt.Errorf("artifact %q changed while hashing", path)
	}
	return agentupdate.Artifact{
		OS:     goos,
		Arch:   goarch,
		Name:   filepath.Base(path),
		Size:   uint64(info.Size()),
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func privateKeysFromEnvironment(name string) ([]ed25519.PrivateKey, error) {
	if name == "" {
		return nil, errors.New("signing key environment variable name is empty")
	}
	value, ok := os.LookupEnv(name)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("signing key environment variable %s is empty", strconv.Quote(name))
	}
	if len(value) > maxSigningKeyEnvironmentBytes {
		return nil, errors.New("signing key environment exceeds size limit")
	}
	return parsePrivateKeys(value)
}

func privateKeysFromFile(path string) ([]ed25519.PrivateKey, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open signing key file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open signing key file: invalid file descriptor")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect opened signing key file: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return nil, errors.New("signing key file must be owned by the current user and have exactly one link")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() <= 0 || info.Size() > maxSigningKeyEnvironmentBytes {
		return nil, errors.New("signing key file must be a non-empty regular file with no group/world permissions")
	}
	return privateKeysFromReader(file)
}

func privateKeysFromReader(reader io.Reader) ([]ed25519.PrivateKey, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxSigningKeyEnvironmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read signing key: %w", err)
	}
	if len(data) == 0 || len(data) > maxSigningKeyEnvironmentBytes {
		return nil, errors.New("signing key input is empty or exceeds size limit")
	}
	return parsePrivateKeys(string(data))
}

func parsePrivateKeys(value string) ([]ed25519.PrivateKey, error) {
	encodedKeys := strings.Fields(strings.ReplaceAll(value, ",", " "))
	if len(encodedKeys) == 0 || len(encodedKeys) > agentupdate.MaxRootKeys {
		return nil, fmt.Errorf("signing key environment must contain 1..%d keys", agentupdate.MaxRootKeys)
	}
	privateKeys := make([]ed25519.PrivateKey, 0, len(encodedKeys))
	for _, encoded := range encodedKeys {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
			return nil, errors.New("signing key is not canonical base64")
		}
		switch len(decoded) {
		case ed25519.SeedSize:
			privateKeys = append(privateKeys, ed25519.NewKeyFromSeed(decoded))
		case ed25519.PrivateKeySize:
			privateKey := ed25519.PrivateKey(decoded)
			seedDerived := ed25519.NewKeyFromSeed(privateKey.Seed())
			if !privateKey.Equal(seedDerived) {
				return nil, errors.New("Ed25519 private key has an inconsistent public suffix")
			}
			privateKeys = append(privateKeys, privateKey)
		default:
			return nil, errors.New("signing key must be a 32-byte Ed25519 seed or 64-byte private key")
		}
	}
	return privateKeys, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %q: %w", path, err)
	}
	success := false
	defer func() {
		if !success {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %q: %w", path, err)
	}
	success = true
	return nil
}
