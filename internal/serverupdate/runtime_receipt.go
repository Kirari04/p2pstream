package serverupdate

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

// runtimeReceipt binds a known, managed creation to the Docker configuration
// actually produced by Compose. A later Compose version may compute another
// config-hash for the same model; only this exact runtime configuration may use
// that compatibility path. A mismatch never updates the receipt.
type runtimeReceipt struct {
	API              int             `json:"api"`
	DeploymentSHA256 string          `json:"deployment_sha256"`
	ImageID          string          `json:"image_id"`
	Fingerprint      json.RawMessage `json:"fingerprint"`
}

const runtimeReceiptFile = "runtime-receipt.json"

// RecordRuntimeReceipt must be called after a trusted managed creation has
// completed and restored its normal restart policy, never to adopt a native
// recreation which failed the pinned Compose hash check.
func (d *ComposeDriver) RecordRuntimeReceipt(ctx context.Context, deployment []byte, containerID string) error {
	if d.Config.DeploymentProtocol != 2 {
		return errors.New("runtime receipts require deployment protocol 2")
	}
	current, err := readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil || hash(current) != hash(deployment) {
		return errors.New("deployment changed before runtime receipt")
	}
	inspection, err := d.receiptInspection(ctx, containerID)
	if err != nil {
		return err
	}
	fieldsRaw, err := d.compose(ctx, "config", "--hash", "p2pstream")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(fieldsRaw))
	labels := receiptMap(receiptMap(inspection["Config"])["Labels"])
	if len(fields) != 2 || fields[0] != "p2pstream" || fields[1] != labels["com.docker.compose.config-hash"] {
		return errors.New("runtime receipt requires the pinned Compose configuration hash")
	}
	service, err := receiptService(deployment)
	if err != nil {
		return err
	}
	image, ok := service["image"].(string)
	if !ok || image == "" {
		return errors.New("runtime receipt requires the canonical image")
	}
	imageRaw, err := d.command(ctx, "image", "inspect", image)
	if err != nil {
		return err
	}
	var images []struct {
		ID string `json:"Id"`
	}
	if err := json.Unmarshal(imageRaw, &images); err != nil || len(images) != 1 || images[0].ID == "" || inspection["Image"] != images[0].ID {
		return errors.New("runtime receipt image differs from canonical image")
	}
	restart, _ := service["restart"].(string)
	if restart == "" {
		restart = "no"
	}
	if receiptMap(receiptMap(inspection["HostConfig"])["RestartPolicy"])["Name"] != restart {
		return errors.New("runtime receipt requires the normal restart policy")
	}
	fingerprint, err := runtimeFingerprint(deployment, inspection)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(runtimeReceipt{API: 1, DeploymentSHA256: hash(deployment), ImageID: images[0].ID, Fingerprint: fingerprint})
	if err != nil {
		return err
	}
	// Native Compose cannot change this root-owned model; still recheck before
	// publishing so another managed transaction cannot inherit a stale receipt.
	current, err = readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil || hash(current) != hash(deployment) {
		return errors.New("deployment changed while recording runtime receipt")
	}
	path := filepath.Join(d.Config.StateDir, runtimeReceiptFile)
	if previous, err := readProtected(path, 2<<20); err == nil && string(previous) == string(encoded) {
		return nil
	}
	return AtomicWrite(path, encoded, 0600)
}

// VerifyRuntimeReceipt is a strict alternative to matching Compose's own hash.
// All callers must retain their normal runtime identity and writer checks.
func (d *ComposeDriver) VerifyRuntimeReceipt(ctx context.Context, deployment []byte, containerID string) error {
	if d.Config.DeploymentProtocol != 2 {
		return errors.New("runtime receipts require deployment protocol 2")
	}
	data, err := readProtected(filepath.Join(d.Config.StateDir, runtimeReceiptFile), 2<<20)
	if err != nil {
		return errors.New("managed runtime receipt is unavailable; use host repair")
	}
	var receipt runtimeReceipt
	if err := decode(data, &receipt); err != nil || receipt.API != 1 || receipt.DeploymentSHA256 != hash(deployment) || receipt.ImageID == "" {
		return errors.New("runtime receipt does not match the current deployment")
	}
	inspection, err := d.receiptInspection(ctx, containerID)
	if err != nil {
		return err
	}
	if inspection["Image"] != receipt.ImageID {
		return errors.New("recreated server image differs from the managed runtime receipt")
	}
	fingerprint, err := runtimeFingerprint(deployment, inspection)
	if err != nil {
		return err
	}
	if string(fingerprint) != string(receipt.Fingerprint) {
		return errors.New("recreated server settings differ from the managed runtime receipt")
	}
	current, err := readProtected(d.Config.DeploymentFile, 2<<20)
	if err != nil || hash(current) != hash(deployment) {
		return errors.New("deployment changed during runtime verification")
	}
	return nil
}

func (d *ComposeDriver) receiptInspection(ctx context.Context, id string) (map[string]any, error) {
	data, err := d.command(ctx, "inspect", id)
	if err != nil {
		return nil, err
	}
	var inspections []map[string]any
	if err := json.Unmarshal(data, &inspections); err != nil || len(inspections) != 1 || inspections[0]["Id"] != id {
		return nil, errors.New("invalid runtime receipt inspection")
	}
	labels := receiptMap(receiptMap(inspections[0]["Config"])["Labels"])
	if labels["com.docker.compose.project"] != d.Config.Project || labels["com.docker.compose.service"] != "p2pstream" || labels["p2pstream.server-update.instance"] != d.Config.InstanceID {
		return nil, errors.New("runtime receipt container is outside the enrolled deployment")
	}
	return inspections[0], nil
}

func receiptMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func receiptService(deployment []byte) (map[string]any, error) {
	var model map[string]any
	if err := json.Unmarshal(deployment, &model); err != nil {
		return nil, err
	}
	service := receiptMap(receiptMap(model["services"])["p2pstream"])
	if service == nil {
		return nil, errors.New("runtime receipt has no canonical server service")
	}
	return service, nil
}

// runtimeFingerprint removes only Docker-generated identity/address fields and
// Compose bookkeeping. All Config and HostConfig fields, including unknown new
// fields, are otherwise retained. This deliberately rejects benign differences
// we have not established as semantically equivalent.
func runtimeFingerprint(deployment []byte, inspection map[string]any) ([]byte, error) {
	service, err := receiptService(deployment)
	if err != nil {
		return nil, err
	}
	// Work on a copy: callers may still need the unmodified Docker inspection.
	encoded, err := json.Marshal(inspection)
	if err != nil {
		return nil, err
	}
	var source map[string]any
	if err := json.Unmarshal(encoded, &source); err != nil {
		return nil, err
	}
	config, host := receiptMap(source["Config"]), receiptMap(source["HostConfig"])
	id, _ := source["Id"].(string)
	if config == nil || host == nil || len(id) < 12 || source["Image"] == nil || receiptMap(source["NetworkSettings"]) == nil {
		return nil, errors.New("incomplete Docker runtime configuration")
	}
	if service["hostname"] == nil && config["Hostname"] == id[:12] {
		config["Hostname"] = "<docker-generated>"
	}
	labels := receiptMap(config["Labels"])
	for _, key := range []string{"com.docker.compose.config-hash", "com.docker.compose.version", "com.docker.compose.project.config_files", "com.docker.compose.project.working_dir", "com.docker.compose.replace", "com.docker.compose.image"} {
		delete(labels, key)
	}
	if err := sortRuntimeStrings(config, "Env", true); err != nil {
		return nil, err
	}
	for _, key := range []string{"Binds", "CapAdd", "CapDrop", "GroupAdd"} {
		if err := sortRuntimeStrings(host, key, false); err != nil {
			return nil, err
		}
	}
	if err := sortRuntimeMounts(host, "Mounts", "Target"); err != nil {
		return nil, err
	}
	if err := sortRuntimeMounts(source, "Mounts", "Destination"); err != nil {
		return nil, err
	}
	networks := receiptMap(receiptMap(source["NetworkSettings"])["Networks"])
	if networks == nil {
		return nil, errors.New("missing Docker network membership")
	}
	for _, value := range networks {
		network := receiptMap(value)
		if network == nil {
			return nil, errors.New("invalid Docker network configuration")
		}
		// IPAMConfig retains requested static IPs and link-local addresses. The
		// assigned fields below reflect dynamic endpoint allocation instead.
		for _, key := range []string{"NetworkID", "EndpointID", "DNSNames"} {
			delete(network, key)
		}
		ipam := receiptMap(network["IPAMConfig"])
		for _, pair := range [][2]string{{"IPv4Address", "IPAddress"}, {"IPv6Address", "GlobalIPv6Address"}} {
			if expected, ok := ipam[pair[0]].(string); ok && expected != "" {
				if network[pair[1]] != expected {
					return nil, errors.New("Docker endpoint differs from its requested static address")
				}
			} else {
				delete(network, pair[1])
			}
		}
		staticMAC := service["mac_address"] != nil
		for _, configured := range receiptMap(service["networks"]) {
			staticMAC = staticMAC || receiptMap(configured)["mac_address"] != nil
		}
		if !staticMAC {
			delete(network, "MacAddress")
		}
		if err := normalizeRuntimeAliases(network, id); err != nil {
			return nil, err
		}
	}
	result := map[string]any{"Config": config, "HostConfig": host, "Mounts": source["Mounts"], "Networks": networks}
	for _, key := range []string{"Image", "Name", "Path", "Args", "AppArmorProfile", "ProcessLabel", "MountLabel", "Platform"} {
		if value, ok := source[key]; ok {
			result[key] = value
		}
	}
	return json.Marshal(result)
}

func normalizeRuntimeAliases(network map[string]any, id string) error {
	value, exists := network["Aliases"]
	if !exists || value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return errors.New("invalid Docker runtime list")
	}
	aliases := make([]string, 0, len(items))
	seen := map[string]bool{}
	for _, item := range items {
		alias, ok := item.(string)
		if !ok {
			return errors.New("invalid Docker runtime list entry")
		}
		// Compose can add the same DNS alias for the container and service
		// names. Only alias membership matters; repeated names add no binding.
		if alias == id || alias == id[:12] || seen[alias] {
			continue
		}
		seen[alias] = true
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	network["Aliases"] = aliases
	return nil
}

func sortRuntimeStrings(object map[string]any, key string, environment bool) error {
	value, exists := object[key]
	if !exists || value == nil {
		return nil
	}
	items, ok := value.([]any)
	if !ok {
		return errors.New("invalid Docker runtime list")
	}
	stringsOnly := make([]string, len(items))
	seen := map[string]bool{}
	for i, item := range items {
		text, ok := item.(string)
		if !ok {
			return errors.New("invalid Docker runtime list entry")
		}
		name := text
		if environment {
			name, _, _ = strings.Cut(text, "=")
		}
		if seen[name] {
			return errors.New("ambiguous duplicate Docker runtime entry")
		}
		seen[name] = true
		stringsOnly[i] = text
	}
	sort.Strings(stringsOnly)
	object[key] = stringsOnly
	return nil
}

func sortRuntimeMounts(object map[string]any, key, targetKey string) error {
	value, exists := object[key]
	if !exists || value == nil {
		return nil
	}
	mounts, ok := value.([]any)
	if !ok {
		return errors.New("invalid Docker runtime mounts")
	}
	seen := map[string]bool{}
	for _, value := range mounts {
		mount := receiptMap(value)
		target, ok := mount[targetKey].(string)
		if !ok || target == "" || seen[target] {
			return errors.New("ambiguous Docker runtime mount")
		}
		seen[target] = true
	}
	sort.Slice(mounts, func(i, j int) bool {
		return receiptMap(mounts[i])[targetKey].(string) < receiptMap(mounts[j])[targetKey].(string)
	})
	object[key] = mounts
	return nil
}
