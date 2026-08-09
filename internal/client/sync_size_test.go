package client

import (
	"strings"
	"testing"

	"github.com/go-johnnyhe/shadow/internal/e2e"
	"github.com/go-johnnyhe/shadow/internal/protocol"
)

func TestWireLimitAllowsValidTenMBEncryptedMessage(t *testing.T) {
	codec, err := e2e.NewCodec("test-key")
	if err != nil {
		t.Fatalf("failed to build codec: %v", err)
	}

	content := strings.Repeat("x", maxSyncedFileBytes)
	operation := protocol.SyncOperation{
		ID:          "client-1",
		Path:        "nested/path/file.txt",
		BaseState:   missingState,
		DesiredHash: fileHash([]byte(content)),
		Content:     []byte(content),
	}
	plaintext, err := protocol.EncodeSyncOperation(operation)
	if err != nil {
		t.Fatalf("failed to encode operation: %v", err)
	}
	encryptedPayload, err := codec.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt payload: %v", err)
	}

	wireMessage := protocol.EncodeEncrypted(encryptedPayload)
	if len(wireMessage) <= maxSyncedFileBytes {
		t.Fatalf("expected encrypted wire message to exceed raw 10MB size, got %d bytes", len(wireMessage))
	}
	if len(wireMessage) > maxIncomingMessageBytes {
		t.Fatalf("valid 10MB file produced %d-byte wire message above %d-byte read limit", len(wireMessage), maxIncomingMessageBytes)
	}
}
