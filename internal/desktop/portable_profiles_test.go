package desktop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func TestPortableAgentProfileExportsOnlyOwnerCreationContractAndRoundTrips(t *testing.T) {
	bridge := newAgentTestBridge(t)
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Yuri", Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	exported, err := bridge.ExportActiveAgentProfile(PortableAgentProfilePathInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if exported.Path != path || exported.Profile.Personalization == nil || exported.Checksum == "" || exported.SizeBytes == 0 {
		t.Fatalf("export view = %#v", exported)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("portable profile mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"credential", "api_key", "permission_grant", "runtime_history", "conversation_id", "memory_id", "revision_id", "agent_id"} {
		if strings.Contains(strings.ToLower(string(data)), forbidden) {
			t.Fatalf("portable profile contains forbidden field %q: %s", forbidden, data)
		}
	}
	opened, err := bridge.OpenPortableAgentProfile(PortableAgentProfilePathInput{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if opened.Checksum != exported.Checksum || !reflect.DeepEqual(opened.Profile, exported.Profile) {
		t.Fatalf("portable round-trip mismatch: exported=%#v opened=%#v", exported, opened)
	}
	created, err := bridge.CreateAgent(opened.Profile)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "owner" || created.Name != exported.Profile.Name {
		t.Fatalf("imported agent = %#v", created)
	}
	seed, err := bridge.repositories.Personalization.Get(context.Background(), domain.ID(created.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seed.Temperament.Traits(), exported.Profile.Traits) || seed.Backstory.Narrative != exported.Profile.Backstory || seed.EvolutionPolicy.ReflectionMaxTokens != exported.Profile.Personalization.EvolutionPolicy.ReflectionMaxTokens {
		t.Fatalf("imported owner seed = %#v", seed)
	}
}

func TestPortableAgentProfileRejectsTamperingAndUnknownAuthorityFieldsWithoutWrites(t *testing.T) {
	bridge := newAgentTestBridge(t)
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Yuri", Gender: "female"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent.json")
	if _, err := bridge.ExportActiveAgentProfile(PortableAgentProfilePathInput{Path: path}); err != nil {
		t.Fatal(err)
	}
	before, _ := bridge.repositories.Agents.List(context.Background())
	data, _ := os.ReadFile(path)
	tampered := bytes.Replace(data, []byte(`"name": "Yuri"`), []byte(`"name": "Tampered"`), 1)
	if bytes.Equal(tampered, data) {
		t.Fatal("test fixture did not contain expected name")
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.OpenPortableAgentProfile(PortableAgentProfilePathInput{Path: path}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("tampered profile error = %v", err)
	}

	unknown := bytes.Replace(data, []byte(`"version": 1,`), []byte(`"version": 1, "permissions": ["filesystem.read"],`), 1)
	if err := os.WriteFile(path, unknown, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.OpenPortableAgentProfile(PortableAgentProfilePathInput{Path: path}); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("authority field error = %v", err)
	}
	after, _ := bridge.repositories.Agents.List(context.Background())
	if len(after) != len(before) {
		t.Fatalf("inspection created agents: before=%d after=%d", len(before), len(after))
	}
}

func TestPortableAgentProfileRefusesSecretLikeOwnerText(t *testing.T) {
	bridge := newAgentTestBridge(t)
	if _, err := bridge.CreateAgent(CreateAgentInput{Name: "Yuri", Gender: "female", Preferences: "api_key=sk-sensitive-value"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "must-not-exist.json")
	if _, err := bridge.ExportActiveAgentProfile(PortableAgentProfilePathInput{Path: path}); !errors.Is(err, domain.ErrNotPermitted) {
		t.Fatalf("secret-like export error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("secret-like export created file: %v", err)
	}
}
