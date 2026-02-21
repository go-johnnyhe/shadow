package client

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/go-johnnyhe/shadow/internal/e2e"
	"github.com/go-johnnyhe/shadow/internal/protocol"
)

func TestDecodeIncomingFileContentEnforcesRawFileLimit(t *testing.T) {
	withinLimit := strings.Repeat("a", maxSyncedFileBytes)
	decoded, err := decodeIncomingFileContent(base64.StdEncoding.EncodeToString([]byte(withinLimit)))
	if err != nil {
		t.Fatalf("expected %d-byte payload to pass, got error: %v", maxSyncedFileBytes, err)
	}
	if len(decoded) != maxSyncedFileBytes {
		t.Fatalf("expected decoded length %d, got %d", maxSyncedFileBytes, len(decoded))
	}

	overLimit := strings.Repeat("b", maxSyncedFileBytes+1)
	_, err = decodeIncomingFileContent(base64.StdEncoding.EncodeToString([]byte(overLimit)))
	if err == nil {
		t.Fatalf("expected over-limit payload to fail")
	}
	if _, ok := err.(incomingFileTooLargeError); !ok {
		t.Fatalf("expected incomingFileTooLargeError, got %T", err)
	}
}

func TestWireLimitAllowsValidTenMBEncryptedMessage(t *testing.T) {
	codec, err := e2e.NewCodec("test-key")
	if err != nil {
		t.Fatalf("failed to build codec: %v", err)
	}

	// Build the same path|base64(payload) plaintext format used by sendFile.
	content := strings.Repeat("x", maxSyncedFileBytes)
	encodedContent := base64.StdEncoding.EncodeToString([]byte(content))
	plaintext := []byte("nested/path/file.txt|" + encodedContent)
	encryptedPayload, err := codec.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("failed to encrypt payload: %v", err)
	}

	wireMessage := []byte(protocol.EncryptedChannel + "|" + encryptedPayload)
	if len(wireMessage) <= maxSyncedFileBytes {
		t.Fatalf("expected encrypted wire message to exceed raw 10MB size, got %d bytes", len(wireMessage))
	}
	if len(wireMessage) > maxIncomingMessageBytes {
		t.Fatalf("valid 10MB file produced %d-byte wire message above %d-byte read limit", len(wireMessage), maxIncomingMessageBytes)
	}
}
