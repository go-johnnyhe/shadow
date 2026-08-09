package protocol

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const (
	WebSocketSubprotocol      = "shadow-v2"
	SyncProtocolVersion       = 2
	ControlChannel            = "__shadow_control__"
	EncryptedChannel          = "__shadow_e2e__"
	OrderedEncryptedChannel   = "__shadow_e2e_ordered__"
	TargetedEncryptedChannel  = "__shadow_e2e_target__"
	BootstrapEncryptedChannel = "__shadow_e2e_bootstrap__"
	SyncDoneChannel           = "__shadow_sync_done__"
	ReadOnlyJoinersKey        = "read_only_joiners"
	PeerCountKey              = "peer_count"
	SyncRequestKey            = "sync_request"
	SyncBaselineKey           = "sync_baseline"
	SyncCompleteKey           = "sync_complete"
	BootstrapManifestType     = "manifest"
)

type SyncOperation struct {
	Version     int    `json:"v"`
	ID          string `json:"id"`
	Path        string `json:"path"`
	BaseState   string `json:"base_state"`
	DesiredHash string `json:"desired_hash"`
	Delete      bool   `json:"delete,omitempty"`
	Content     []byte `json:"content,omitempty"`
}

type BootstrapManifest struct {
	Version     int      `json:"v"`
	Type        string   `json:"type"`
	Paths       []string `json:"paths"`
	Directories []string `json:"directories,omitempty"`
	SingleFile  string   `json:"single_file,omitempty"`
}

func EncodeSyncOperation(operation SyncOperation) ([]byte, error) {
	operation.Version = SyncProtocolVersion
	return json.Marshal(operation)
}

func DecodeSyncOperation(payload []byte) (SyncOperation, error) {
	var operation SyncOperation
	if err := json.Unmarshal(payload, &operation); err != nil {
		return SyncOperation{}, fmt.Errorf("invalid sync operation: %w", err)
	}
	if operation.Version != SyncProtocolVersion {
		return SyncOperation{}, fmt.Errorf("unsupported sync protocol version %d", operation.Version)
	}
	if operation.ID == "" || operation.Path == "" || operation.BaseState == "" || operation.DesiredHash == "" {
		return SyncOperation{}, fmt.Errorf("incomplete sync operation")
	}
	if !validOperationID(operation.ID) {
		return SyncOperation{}, fmt.Errorf("invalid sync operation ID")
	}
	return operation, nil
}

func validOperationID(operationID string) bool {
	if operationID == "" || len(operationID) > 96 {
		return false
	}
	for _, r := range operationID {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func EncodeBootstrapManifest(paths, directories []string, singleFile string) ([]byte, error) {
	return json.Marshal(BootstrapManifest{
		Version:     SyncProtocolVersion,
		Type:        BootstrapManifestType,
		Paths:       paths,
		Directories: directories,
		SingleFile:  singleFile,
	})
}

func DecodeBootstrapManifest(payload []byte) (BootstrapManifest, bool, error) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(payload, &header); err != nil || header.Type != BootstrapManifestType {
		return BootstrapManifest{}, false, nil
	}
	var manifest BootstrapManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return BootstrapManifest{}, true, fmt.Errorf("invalid bootstrap manifest: %w", err)
	}
	if manifest.Version != SyncProtocolVersion || len(manifest.Paths) > 100000 || len(manifest.Directories) > len(manifest.Paths) {
		return BootstrapManifest{}, true, fmt.Errorf("invalid bootstrap manifest")
	}
	return manifest, true, nil
}

func EncodeEncrypted(payload string) []byte {
	return []byte(EncryptedChannel + "|" + payload)
}

func EncodeTargetedEncrypted(peerID, payload string) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s", TargetedEncryptedChannel, peerID, payload))
}

func ParseTargetedEncrypted(message []byte) (string, string, bool) {
	parts := strings.SplitN(string(message), "|", 3)
	if len(parts) != 3 || parts[0] != TargetedEncryptedChannel || !validPeerID(parts[1]) || parts[2] == "" {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func EncodeOrderedEncrypted(sequence uint64, encryptedPayload string) []byte {
	return []byte(fmt.Sprintf("%s|%d|%s", OrderedEncryptedChannel, sequence, encryptedPayload))
}

func ParseOrderedEncrypted(message []byte) (uint64, string, bool) {
	parts := strings.SplitN(string(message), "|", 3)
	if len(parts) != 3 || parts[0] != OrderedEncryptedChannel || parts[2] == "" {
		return 0, "", false
	}
	sequence, err := strconv.ParseUint(parts[1], 10, 64)
	if err != nil || sequence == 0 {
		return 0, "", false
	}
	return sequence, parts[2], true
}

func EncodeBootstrapEncrypted(encryptedPayload string) []byte {
	return []byte(BootstrapEncryptedChannel + "|" + encryptedPayload)
}

func ParseBootstrapEncrypted(message []byte) (string, bool) {
	prefix := BootstrapEncryptedChannel + "|"
	if !strings.HasPrefix(string(message), prefix) || len(message) == len(prefix) {
		return "", false
	}
	return string(message[len(prefix):]), true
}

func EncodeSyncDone(peerID string) []byte {
	return []byte(SyncDoneChannel + "|" + peerID)
}

func ParseSyncDone(message []byte) (string, bool) {
	parts := strings.SplitN(string(message), "|", 2)
	if len(parts) != 2 || parts[0] != SyncDoneChannel || !validPeerID(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func validPeerID(peerID string) bool {
	if peerID == "" || len(peerID) > 64 {
		return false
	}
	for _, r := range peerID {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func EncodeControlReadOnlyJoiners(enabled bool) []byte {
	value := "0"
	if enabled {
		value = "1"
	}
	return []byte(fmt.Sprintf("%s|%s=%s", ControlChannel, ReadOnlyJoinersKey, value))
}

func ParseReadOnlyJoinersControl(payload string) (bool, bool) {
	key, value, ok := strings.Cut(payload, "=")
	if !ok || key != ReadOnlyJoinersKey {
		return false, false
	}

	n, err := strconv.Atoi(value)
	if err != nil {
		return false, false
	}
	return n == 1, true
}

func EncodeControlPeerCount(count int) []byte {
	return []byte(fmt.Sprintf("%s|%s=%d", ControlChannel, PeerCountKey, count))
}

func ParsePeerCountControl(payload string) (int, bool) {
	key, value, ok := strings.Cut(payload, "=")
	if !ok || key != PeerCountKey {
		return 0, false
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return n, true
}

func EncodeControlSyncRequest(peerID string) []byte {
	return []byte(fmt.Sprintf("%s|%s=%s", ControlChannel, SyncRequestKey, peerID))
}

func EncodeControlSyncBaseline(sequence uint64) []byte {
	return []byte(fmt.Sprintf("%s|%s=%d", ControlChannel, SyncBaselineKey, sequence))
}

func ParseSyncBaselineControl(payload string) (uint64, bool) {
	key, value, ok := strings.Cut(payload, "=")
	if !ok || key != SyncBaselineKey {
		return 0, false
	}
	sequence, err := strconv.ParseUint(value, 10, 64)
	return sequence, err == nil
}

func ParseSyncRequestControl(payload string) (string, bool) {
	key, peerID, ok := strings.Cut(payload, "=")
	if !ok || key != SyncRequestKey || !validPeerID(peerID) {
		return "", false
	}
	return peerID, true
}

func EncodeControlSyncComplete() []byte {
	return []byte(fmt.Sprintf("%s|%s=1", ControlChannel, SyncCompleteKey))
}

func ParseSyncCompleteControl(payload string) bool {
	key, value, ok := strings.Cut(payload, "=")
	return ok && key == SyncCompleteKey && value == "1"
}
