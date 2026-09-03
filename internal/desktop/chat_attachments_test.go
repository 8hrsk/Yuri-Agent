package desktop

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/config"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

func encodedAttachment(id, name, mediaType string, data []byte) ChatAttachmentInput {
	return ChatAttachmentInput{ID: id, Name: name, MediaType: mediaType, SizeBytes: int64(len(data)), DataBase64: base64.StdEncoding.EncodeToString(data)}
}

func TestPrepareChatAttachmentsAcceptsUTF8CodeAndSupportedImages(t *testing.T) {
	root := t.TempDir()
	bridge := &Bridge{paths: config.Paths{BlobDirectory: filepath.Join(root, "blobs")}}
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)
	items, err := bridge.prepareChatAttachments([]ChatAttachmentInput{
		encodedAttachment("attachment-code", "main.go", "", []byte("package main\nfunc main() {}\n")),
		encodedAttachment("attachment-image", "screen.png", "image/png", png),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].Kind != "text" || !strings.HasPrefix(items[0].MediaType, "text/") || items[1].Kind != "image" || items[1].MediaType != "image/png" {
		t.Fatalf("attachments = %#v", items)
	}
	for _, item := range items {
		if _, err := os.Stat(filepath.Join(bridge.paths.BlobDirectory, filepath.FromSlash(item.BlobKey))); err != nil {
			t.Fatalf("blob %q was not persisted: %v", item.BlobKey, err)
		}
	}
	metadata, err := attachmentMetadataJSON(items)
	if err != nil {
		t.Fatal(err)
	}
	message := bridge.messageForModel(storage.Message{Content: "Проверь файлы", Role: "user", ProviderMeta: metadata}, true)
	if !strings.Contains(message.Content, "package main") || !strings.Contains(message.Content, "untrusted user data") {
		t.Fatalf("text attachment envelope = %q", message.Content)
	}
	if len(message.Parts) != 1 || message.Parts[0].Name != "screen.png" || message.Parts[0].Data == "" {
		t.Fatalf("image parts = %#v", message.Parts)
	}
}

func TestTranscriptForModelIncludesCurrentImageAfterOlderUserTurns(t *testing.T) {
	bridge := &Bridge{paths: config.Paths{BlobDirectory: filepath.Join(t.TempDir(), "blobs")}}
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	items, err := bridge.prepareChatAttachments([]ChatAttachmentInput{
		encodedAttachment("attachment-current", "screen.png", "image/png", png),
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := attachmentMetadataJSON(items)
	if err != nil {
		t.Fatal(err)
	}
	messages := []storage.Message{
		{ID: "message-old-user", Role: "user", Content: "Привет"},
		{ID: "message-old-assistant", Role: "assistant", Content: "Привет"},
		{ID: "message-current-user", Role: "user", Content: "Что на изображении?", ProviderMeta: metadata},
	}

	payloadID := attachmentPayloadMessageID(messages, "message-current-user", "")
	transcript := bridge.transcriptForModel(messages, payloadID)
	if len(transcript) != 3 || len(transcript[2].Parts) != 1 {
		t.Fatalf("current image was lost after older turns: %#v", transcript)
	}
	if transcript[2].Parts[0].Name != "screen.png" || transcript[2].Parts[0].Data == "" {
		t.Fatalf("current image payload = %#v", transcript[2].Parts)
	}
}

func TestAttachmentPayloadMessageIDReusesOriginalUserImageOnRetry(t *testing.T) {
	messages := []storage.Message{
		{ID: "message-user", Role: "user", Content: "Что на изображении?"},
		{ID: "message-assistant", Role: "assistant", Content: "Первый ответ"},
		{ID: "message-later-user", Role: "user", Content: "Позже"},
	}
	if got := attachmentPayloadMessageID(messages, "", "message-assistant"); got != "message-user" {
		t.Fatalf("retry attachment payload id = %q, want message-user", got)
	}
	if got := attachmentPayloadMessageID(messages, "", "message-user"); got != "message-user" {
		t.Fatalf("trace retry attachment payload id = %q, want message-user", got)
	}
}

func TestPrepareChatAttachmentsRejectsBinaryAndSpoofedPayloads(t *testing.T) {
	bridge := &Bridge{paths: config.Paths{BlobDirectory: filepath.Join(t.TempDir(), "blobs")}}
	tests := []struct {
		name  string
		input ChatAttachmentInput
	}{
		{name: "blocked extension", input: encodedAttachment("attachment-bin", "helper.dll", "text/plain", []byte("MZ"))},
		{name: "nul payload", input: encodedAttachment("attachment-nul", "payload.dat", "", []byte{'a', 0, 'b'})},
		{name: "spoofed image", input: encodedAttachment("attachment-image", "screen.png", "image/png", []byte("not an image"))},
		{name: "unsafe id", input: encodedAttachment("../attachment", "notes.md", "text/plain", []byte("notes"))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := bridge.prepareChatAttachments([]ChatAttachmentInput{test.input}); err == nil {
				t.Fatal("attachment was unexpectedly accepted")
			}
		})
	}
}
