package smoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/backup"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	"github.com/OrdoAI/yuri-agent/internal/memory"
	"github.com/OrdoAI/yuri-agent/internal/plugins"
	schedulerpkg "github.com/OrdoAI/yuri-agent/internal/scheduler"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// TestMVPOfflineLifecycle is a bounded dogfooding path for the local MVP. It
// deliberately shares one temporary SQLite database between sequential
// subtests so the test exercises durable hand-offs rather than isolated mocks.
// No network, keyring, GUI, or provider credentials are involved.
func TestMVPOfflineLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	testRoot := t.TempDir()
	databasePath := filepath.Join(testRoot, "yuri.sqlite3")
	database, err := storage.Open(ctx, databasePath)
	if err != nil {
		t.Fatalf("open temporary sqlite: %v", err)
	}
	defer database.Close()
	repositories, err := storage.NewRepositories(database)
	if err != nil {
		t.Fatalf("construct sqlite repositories: %v", err)
	}

	conversationID := domain.ID("conversation-dogfood")
	messageID := domain.ID("message-dogfood")
	memoryID := domain.ID("memory-dogfood")
	personaID := domain.ID("persona-dogfood")
	relationshipID := domain.ID("relationship-dogfood")
	affectID := domain.ID("affect-dogfood")
	pluginID := domain.ID("yuri.reference-echo")

	t.Run("conversation archive and memory", func(t *testing.T) {
		conversation := storage.Conversation{
			ID: conversationID, Title: "Offline dogfood", CreatedAt: now, UpdatedAt: now,
		}
		if err := repositories.Conversations.Create(ctx, conversation); err != nil {
			t.Fatalf("create conversation: %v", err)
		}
		message := storage.Message{
			ID: messageID, ConversationID: conversationID, Role: "user",
			Content: "The local-first dogfooding transcript is durable.", Status: "complete",
			ProviderMeta: `{"provider":"offline"}`, CreatedAt: now,
		}
		if err := repositories.Messages.Create(ctx, message); err != nil {
			t.Fatalf("create message: %v", err)
		}
		storedMessage, err := repositories.Messages.Get(ctx, messageID)
		if err != nil {
			t.Fatalf("read message: %v", err)
		}
		if storedMessage.ConversationID != conversationID || storedMessage.Content != message.Content {
			t.Fatalf("message round trip mismatch: %#v", storedMessage)
		}
		archiveHits, err := repositories.Archive.Search(ctx, "durable", storage.ArchiveSearchOptions{Limit: 4})
		if err != nil {
			t.Fatalf("search transcript archive: %v", err)
		}
		if len(archiveHits) == 0 || archiveHits[0].Message.ID != messageID {
			t.Fatalf("expected archived message, got %#v", archiveHits)
		}

		adapter := sqliteMemoryStore{repositories: repositories}
		engine, err := memory.NewEngine(memory.Config{
			Store: adapter, Lexical: adapter, Archive: adapter,
			Now:          func() time.Time { return now },
			Ranker:       memory.HybridRanker{Weights: memory.DefaultRankWeights()},
			CoreBudget:   memory.Budget{MaxItems: 4, MaxChars: 512},
			RecallBudget: memory.Budget{MaxItems: 4, MaxChars: 512},
		})
		if err != nil {
			t.Fatalf("construct memory engine: %v", err)
		}
		written, err := engine.Remember(ctx, memory.Candidate{
			Operation: memory.CandidateCreate, DedupKey: "profile:dogfood",
			Reason: "offline lifecycle dogfood",
			Memory: domain.Memory{
				ID: memoryID, Kind: domain.MemoryKindCore, Nature: domain.MemoryNatureFact,
				Content: "Owner prefers local offline dogfooding.", Confidence: 0.95, Salience: 0.9,
				Sensitivity: domain.MemorySensitivityPrivate, Retention: domain.MemoryRetentionPermanent,
				Lifecycle: domain.MemoryLifecycleActive, CreatedAt: now, UpdatedAt: now,
			},
			Sources: []domain.MemorySource{{
				SourceType: "message", SourceID: messageID, ConversationID: conversationID,
				MessageID: messageID, CreatedAt: now,
			}},
		})
		if err != nil {
			t.Fatalf("remember memory: %v", err)
		}
		if !written.Created || written.Memory.ID != memoryID || written.Memory.Version != 1 {
			t.Fatalf("unexpected memory write: %#v", written)
		}
		recalled, err := engine.Recall(ctx, "dogfooding", memory.RecallOptions{
			Mode: memory.RecallAutomatic, Limit: 2, Now: now,
			Budget: memory.Budget{MaxItems: 2, MaxChars: 256},
		})
		if err != nil {
			t.Fatalf("recall memory: %v", err)
		}
		if len(recalled) == 0 || recalled[0].Memory.ID != memoryID {
			t.Fatalf("expected recalled memory, got %#v", recalled)
		}
		if len(recalled[0].Evidence.Sources) == 0 || recalled[0].Evidence.Sources[0].MessageID != messageID {
			t.Fatalf("memory provenance was not retained: %#v", recalled[0].Evidence)
		}
		snapshot, err := engine.CoreSnapshot(ctx, memory.Budget{MaxItems: 2, MaxChars: 256})
		if err != nil {
			t.Fatalf("build core snapshot: %v", err)
		}
		if len(snapshot.Entries) != 1 || snapshot.Entries[0].Memory.ID != memoryID {
			t.Fatalf("unexpected core snapshot: %#v", snapshot)
		}
		versions, err := repositories.Memories.ListVersions(ctx, memoryID, 0)
		if err != nil {
			t.Fatalf("list memory versions: %v", err)
		}
		if len(versions) < 2 {
			t.Fatalf("recall should persist a touch revision, got %d versions", len(versions))
		}
		memoryArchiveHits, err := adapter.SearchArchive(ctx, "durable", memory.ArchiveSearchOptions{Limit: 2})
		if err != nil {
			t.Fatalf("memory archive bridge: %v", err)
		}
		if len(memoryArchiveHits) == 0 || memoryArchiveHits[0].MessageID != messageID {
			t.Fatalf("archive bridge did not find transcript: %#v", memoryArchiveHits)
		}
	})

	t.Run("persona relationship and affect", func(t *testing.T) {
		evidence := []domain.EvidenceLink{{
			SourceType: "message", SourceID: messageID, ConversationID: conversationID,
			MessageID: messageID, CreatedAt: now,
		}}
		persona, err := domain.NewMutablePersona(personaID, map[string]float64{
			string(domain.TraitWarmth): 0.70, string(domain.TraitTrust): 0.65,
		}, "Warm, direct, local-first.", now)
		if err != nil {
			t.Fatalf("create persona snapshot: %v", err)
		}
		if err := repositories.Persona.CreateWithMetadata(ctx, persona, storage.PersonaVersionMetadata{
			RevisionID: domain.ID("persona-dogfood:v1"), Operation: domain.PersonaOperationCreate,
			Reason: "offline seed", Evidence: evidence,
		}); err != nil {
			t.Fatalf("persist persona: %v", err)
		}
		personaNext := persona
		personaNext.Version = 2
		personaNext.UpdatedAt = now.Add(time.Second)
		personaNext.Traits[string(domain.TraitWarmth)] = 0.75
		personaNext.Reason = "offline evidence update"
		personaNext.Evidence = evidence
		if _, err := repositories.Persona.AppendVersionWithMetadata(ctx, personaNext, 1, storage.PersonaVersionMetadata{
			RevisionID: domain.ID("persona-dogfood:v2"), Operation: domain.PersonaOperationUpdate,
			Reason: "offline evidence update", Evidence: evidence,
		}); err != nil {
			t.Fatalf("append persona revision: %v", err)
		}
		personaVersions, err := repositories.Persona.ListVersions(ctx, personaID, 0)
		if err != nil || len(personaVersions) != 2 {
			t.Fatalf("persona history: versions=%d err=%v", len(personaVersions), err)
		}

		relationship, err := domain.NewRelationshipState(relationshipID, map[string]float64{
			domain.RelationshipDimensionTrust: 0.70,
		}, "Reliable local collaboration.", now)
		if err != nil {
			t.Fatalf("create relationship snapshot: %v", err)
		}
		if err := repositories.Relationship.CreateWithMetadata(ctx, relationship, storage.RelationshipVersionMetadata{
			RevisionID: domain.ID("relationship-dogfood:v1"), Operation: domain.RelationshipOperationCreate,
			Reason: "offline seed", Evidence: evidence,
		}); err != nil {
			t.Fatalf("persist relationship: %v", err)
		}
		relationshipNext := relationship
		relationshipNext.Version = 2
		relationshipNext.UpdatedAt = now.Add(2 * time.Second)
		relationshipNext.Dimensions[domain.RelationshipDimensionTrust] = 0.75
		relationshipNext.Reason = "offline evidence update"
		relationshipNext.Evidence = evidence
		if _, err := repositories.Relationship.AppendVersionWithMetadata(ctx, relationshipNext, 1, storage.RelationshipVersionMetadata{
			RevisionID: domain.ID("relationship-dogfood:v2"), Operation: domain.RelationshipOperationUpdate,
			Reason: "offline evidence update", Evidence: evidence,
		}); err != nil {
			t.Fatalf("append relationship revision: %v", err)
		}

		affect, err := domain.NewAffectiveState(affectID, map[string]float64{
			domain.EmotionTenderness: 0.20,
		}, "Steady offline affect.", now)
		if err != nil {
			t.Fatalf("create affect snapshot: %v", err)
		}
		if err := repositories.Affect.CreateStateWithMetadata(ctx, affect, storage.AffectiveVersionMetadata{
			RevisionID: domain.ID("affect-dogfood:v1"), Operation: domain.AffectOperationCreate,
			Reason: "offline seed",
		}); err != nil {
			t.Fatalf("persist affect: %v", err)
		}
		eventState, err := repositories.Affect.AppendEvent(ctx, affectID, uint64(1), domain.AffectiveEvent{
			ID: domain.ID("affect-event-dogfood"), Emotion: domain.EmotionJoy,
			Intensity: 0.8, Valence: 1, DecayPolicy: domain.AffectiveDecayNone,
			SourceType: "message", SourceID: messageID, ConversationID: conversationID,
			Evidence: evidence, CreatedAt: now.Add(3 * time.Second),
		})
		if err != nil {
			t.Fatalf("append affect event: %v", err)
		}
		if eventState.Version != 2 || eventState.Emotions[domain.EmotionJoy] <= 0 {
			t.Fatalf("affect event did not advance state: %#v", eventState)
		}
		events, err := repositories.Affect.ListEvents(ctx, affectID)
		if err != nil || len(events) != 1 || events[0].ID != domain.ID("affect-event-dogfood") {
			t.Fatalf("affect event history: events=%d err=%v", len(events), err)
		}
	})

	t.Run("scheduler one shot", func(t *testing.T) {
		clock := fixedClock{now: now}
		var executions atomic.Int32
		worker, err := schedulerpkg.New(repositories.Scheduler, schedulerpkg.ExecuteFunc(func(ctx context.Context, job schedulerpkg.ScheduledJob) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if job.Schedule.PayloadJSON != `{"kind":"dogfood"}` {
				return fmt.Errorf("unexpected scheduler payload %s", job.Schedule.PayloadJSON)
			}
			executions.Add(1)
			return nil
		}), schedulerpkg.Options{
			Clock: clock, WorkerID: "dogfood-worker", LeaseDuration: time.Minute,
			PollInterval: time.Hour, MaxClaimsPerCycle: 1,
		})
		if err != nil {
			t.Fatalf("construct scheduler: %v", err)
		}
		scheduleID := domain.ID("schedule-dogfood")
		if err := worker.CreateSchedule(ctx, domain.Schedule{
			ID: scheduleID, Name: "Offline dogfood", Kind: domain.ScheduleKindOnce,
			Timezone: "UTC", StartAt: now.Add(-time.Minute),
			PayloadJSON: `{"kind":"dogfood"}`, Status: domain.ScheduleStatusActive,
			Enabled: true, MisfirePolicy: domain.MisfireRunOnce, NextRunAt: now.Add(-time.Second),
			Retry: domain.RetryPolicy{MaxAttempts: 1}, Budget: domain.JobBudget{}, HistoryLimit: 10,
			CreatedAt: now, UpdatedAt: now, Version: 1,
		}); err != nil {
			t.Fatalf("create one-shot schedule: %v", err)
		}
		result, err := worker.RunOnce(ctx)
		if err != nil {
			t.Fatalf("run one scheduler cycle: %v", err)
		}
		if result.Claimed != 1 || result.Completed != 1 || executions.Load() != 1 {
			t.Fatalf("unexpected scheduler result: %#v executions=%d", result, executions.Load())
		}
		schedule, err := worker.GetSchedule(ctx, scheduleID)
		if err != nil {
			t.Fatalf("read completed schedule: %v", err)
		}
		if schedule.Status != domain.ScheduleStatusCompleted || schedule.Enabled {
			t.Fatalf("one-shot schedule was not completed: %#v", schedule)
		}
		runs, err := worker.ListJobRuns(ctx, scheduleID, domain.JobRunListOptions{Limit: 2})
		if err != nil || len(runs) != 1 || runs[0].State != domain.JobRunSucceeded {
			t.Fatalf("scheduler run history: runs=%#v err=%v", runs, err)
		}
	})

	t.Run("reference plugin", func(t *testing.T) {
		repositoryRoot := repositoryRoot(t)
		manifestPath := filepath.Join(repositoryRoot, "plugins", "reference", plugins.ManifestFileName)
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatalf("read reference manifest: %v", err)
		}
		var manifest plugins.Manifest
		if err := json.Unmarshal(manifestContent, &manifest); err != nil {
			t.Fatalf("decode reference manifest: %v", err)
		}
		packageDirectory := t.TempDir()
		binDirectory := filepath.Join(packageDirectory, "bin")
		if err := os.MkdirAll(binDirectory, 0o700); err != nil {
			t.Fatalf("create plugin bin directory: %v", err)
		}
		executable := filepath.Join(binDirectory, "yuri-reference")
		command := exec.Command("go", "build", "-o", executable, "./plugins/reference")
		command.Dir = repositoryRoot
		command.Env = append(os.Environ(), "CGO_ENABLED=0")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build reference plugin: %v\n%s", err, output)
		}
		if err := os.Chmod(executable, 0o700); err != nil {
			t.Fatalf("mark reference plugin executable: %v", err)
		}
		if err := repositories.Plugins.Upsert(ctx, storage.PluginRecord{
			ID: pluginID, Name: manifest.Name, Publisher: manifest.Publisher,
			Version: manifest.Version, ProtocolVersion: manifest.ProtocolVersion,
			Enabled: false, InstallPath: packageDirectory, ManifestJSON: string(manifestContent),
			SignatureStatus: "dev-unsigned", RuntimeStatus: string(plugins.StateStopped),
			InstalledAt: now, UpdatedAt: now,
		}, storage.PluginSource{PluginID: pluginID, CheckedAt: now}); err != nil {
			t.Fatalf("persist plugin metadata: %v", err)
		}

		supervisor, err := plugins.NewSupervisor(plugins.SupervisorConfig{
			Manifest: manifest, PackageDir: packageDirectory, CoreVersion: "0.4.0",
			DevMode: true, Client: plugins.ClientConfig{CloseTimeout: time.Second},
		})
		if err != nil {
			t.Fatalf("construct reference supervisor: %v", err)
		}
		if err := supervisor.Start(ctx); err != nil {
			t.Fatalf("start reference supervisor: %v", err)
		}
		defer supervisor.Stop(context.Background())
		if err := repositories.Plugins.SetRuntimeStatus(ctx, pluginID, string(plugins.StateRunning), "", now); err != nil {
			t.Fatalf("persist running plugin status: %v", err)
		}
		if err := repositories.Plugins.SetEnabled(ctx, pluginID, true, now); err != nil {
			t.Fatalf("enable plugin metadata: %v", err)
		}
		state, stateErr := supervisor.State()
		if stateErr != nil || state != plugins.StateRunning {
			t.Fatalf("reference supervisor state: %s err=%v", state, stateErr)
		}
		result, err := supervisor.InvokeTool(ctx, plugins.ToolInvokeParams{
			ToolID: "echo", Arguments: json.RawMessage(`{"message":"offline dogfood"}`),
		})
		if err != nil {
			t.Fatalf("invoke reference echo: %v", err)
		}
		if !result.OK || string(result.Output) != `{"message":"offline dogfood"}` {
			t.Fatalf("unexpected reference response: %#v", result)
		}
		if err := supervisor.Stop(ctx); err != nil {
			t.Fatalf("stop reference supervisor: %v", err)
		}
		if err := repositories.Plugins.SetRuntimeStatus(ctx, pluginID, string(plugins.StateStopped), "", now.Add(time.Second)); err != nil {
			t.Fatalf("persist stopped plugin status: %v", err)
		}
		storedPlugin, err := repositories.Plugins.Get(ctx, pluginID)
		if err != nil || !storedPlugin.Enabled || storedPlugin.RuntimeStatus != string(plugins.StateStopped) {
			t.Fatalf("plugin metadata lifecycle: %#v err=%v", storedPlugin, err)
		}
	})

	t.Run("encrypted backup restore", func(t *testing.T) {
		repositoryRoot := repositoryRoot(t)
		manifestPath := filepath.Join(repositoryRoot, "plugins", "reference", plugins.ManifestFileName)
		archivePath := filepath.Join(testRoot, "dogfood.yuribackup")
		const passphrase = "offline-dogfood-passphrase"
		_, err := backup.Export(ctx, database, archivePath, passphrase, backup.ExportOptions{
			ConfigMetadata: map[string]any{
				"profile": "dogfood", "api_key": "must-not-export", "providers": []any{
					map[string]any{"model": "offline", "api_key": "must-not-export"},
				},
			},
			Blobs: []backup.Blob{{Name: "reference-plugin.json", Source: manifestPath}},
		})
		if err != nil {
			t.Fatalf("export encrypted backup: %v", err)
		}
		manifest, err := backup.Validate(ctx, archivePath, passphrase, backup.RestoreOptions{})
		if err != nil {
			t.Fatalf("validate encrypted backup: %v", err)
		}
		if manifest.Database.Path != "database.sqlite3" || manifest.Config == nil || len(manifest.Blobs) != 1 {
			t.Fatalf("unexpected backup manifest: %#v", manifest)
		}
		restoreDir := filepath.Join(testRoot, "restored")
		result, err := backup.RestoreToTemp(ctx, archivePath, passphrase, restoreDir, backup.RestoreOptions{})
		if err != nil {
			t.Fatalf("restore encrypted backup: %v", err)
		}
		if result.DatabasePath == "" || result.ConfigPath == "" || len(result.BlobPaths) != 1 {
			t.Fatalf("unexpected restore result: %#v", result)
		}
		config, err := os.ReadFile(result.ConfigPath)
		if err != nil {
			t.Fatalf("read restored config: %v", err)
		}
		if strings.Contains(string(config), "api_key") || strings.Contains(string(config), "must-not-export") {
			t.Fatalf("restored config retained secret-shaped metadata: %s", config)
		}
		restoredDB, err := storage.Open(ctx, result.DatabasePath)
		if err != nil {
			t.Fatalf("open restored sqlite: %v", err)
		}
		defer restoredDB.Close()
		restoredRepositories, err := storage.NewRepositories(restoredDB)
		if err != nil {
			t.Fatalf("construct restored repositories: %v", err)
		}
		if _, err := restoredRepositories.Conversations.Get(ctx, conversationID); err != nil {
			t.Fatalf("restored conversation: %v", err)
		}
		if _, err := restoredRepositories.Memories.Get(ctx, memoryID); err != nil {
			t.Fatalf("restored memory: %v", err)
		}
		if _, err := restoredRepositories.Persona.Get(ctx, personaID); err != nil {
			t.Fatalf("restored persona: %v", err)
		}
		if _, err := restoredRepositories.Plugins.Get(ctx, pluginID); err != nil {
			t.Fatalf("restored plugin metadata: %v", err)
		}
	})
}

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve smoke test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

// sqliteMemoryStore is the provider-neutral memory.Store port backed by the
// same real repositories used by the desktop runtime. Keeping this adapter in
// the smoke package avoids coupling the test to desktop's unexported wiring.
type sqliteMemoryStore struct{ repositories *storage.Repositories }

func (adapter sqliteMemoryStore) GetMemory(ctx context.Context, id domain.ID) (domain.Memory, error) {
	return adapter.repositories.Memories.Get(ctx, id)
}

func (adapter sqliteMemoryStore) ListMemories(ctx context.Context, filter memory.MemoryFilter) ([]domain.Memory, error) {
	items, err := adapter.repositories.Memories.List(ctx, storage.MemoryListOptions{
		IncludeDormant: filter.IncludeDormant, IncludeDeleted: filter.IncludeDeleted,
		IncludeHidden: filter.IncludeHidden, Limit: filter.Limit,
	})
	if err != nil {
		return nil, err
	}
	kinds := make(map[domain.MemoryKind]struct{}, len(filter.Kinds))
	for _, kind := range filter.Kinds {
		kinds[kind] = struct{}{}
	}
	states := make(map[domain.MemoryLifecycle]struct{}, len(filter.States))
	for _, state := range filter.States {
		states[state] = struct{}{}
	}
	result := make([]domain.Memory, 0, len(items))
	for _, item := range items {
		if len(kinds) > 0 {
			if _, ok := kinds[item.Kind]; !ok {
				continue
			}
		}
		if len(states) > 0 {
			if _, ok := states[item.Lifecycle]; !ok {
				continue
			}
		}
		if item.HiddenFromCore && !filter.IncludeHidden {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}

func (adapter sqliteMemoryStore) ApplyMemoryChange(ctx context.Context, change memory.MemoryChange) error {
	if change.Memory.ID.Empty() {
		return fmt.Errorf("memory id is required")
	}
	current, err := adapter.repositories.Memories.Get(ctx, change.Memory.ID)
	if errors.Is(err, domain.ErrNotFound) {
		if change.Memory.Version != 1 {
			return domain.ErrConflict
		}
		return adapter.repositories.Memories.Create(ctx, change.Memory, change.Sources)
	}
	if err != nil {
		return err
	}
	metadata := storage.MemoryVersionMetadata{
		ParentVersion: current.Version, Operation: "update", Reason: change.Memory.Reason,
	}
	if change.Revision != nil {
		metadata.RevisionID = change.Revision.ID
		metadata.ParentVersion = change.Revision.ParentVersion
		metadata.Operation = string(change.Revision.Operation)
		metadata.Reason = change.Revision.Reason
	}
	_, err = adapter.repositories.Memories.AppendVersionWithMetadata(ctx, change.Memory, current.Version, metadata, change.Sources)
	return err
}

func (adapter sqliteMemoryStore) TouchMemory(ctx context.Context, id domain.ID, at time.Time) error {
	_, err := adapter.repositories.Memories.RecordRecall(ctx, id, at)
	return err
}

func (adapter sqliteMemoryStore) ListMemorySources(ctx context.Context, id domain.ID) ([]domain.MemorySource, error) {
	return adapter.repositories.Memories.ListSources(ctx, id)
}

func (adapter sqliteMemoryStore) ListMemoryVersions(ctx context.Context, id domain.ID, limit int) ([]memory.MemoryRevision, error) {
	versions, err := adapter.repositories.Memories.ListVersions(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	result := make([]memory.MemoryRevision, 0, len(versions))
	for _, version := range versions {
		result = append(result, memory.MemoryRevision{
			ID: version.RevisionID, MemoryID: version.Memory.ID,
			Operation: memory.MemoryOperation(version.Operation), Snapshot: version.Memory,
			ParentVersion: version.ParentVersion, Reason: version.Reason,
			CreatedAt: version.Memory.UpdatedAt,
		})
	}
	return result, nil
}

func (adapter sqliteMemoryStore) SearchMemoryLexical(ctx context.Context, query string, filter memory.MemoryFilter, limit int) ([]memory.LexicalHit, error) {
	hits, err := adapter.repositories.Memories.Search(ctx, query, storage.MemorySearchOptions{
		IncludeDormant: filter.IncludeDormant, IncludeDeleted: filter.IncludeDeleted,
		IncludeHidden: filter.IncludeHidden, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	result := make([]memory.LexicalHit, 0, len(hits))
	for _, hit := range hits {
		result = append(result, memory.LexicalHit{MemoryID: hit.Memory.ID, Score: hit.Score, Snippet: hit.Snippet})
	}
	return result, nil
}

func (adapter sqliteMemoryStore) SearchArchive(ctx context.Context, query string, options memory.ArchiveSearchOptions) ([]memory.ArchiveHit, error) {
	hits, err := adapter.repositories.Archive.Search(ctx, query, storage.ArchiveSearchOptions{
		IncludeArchived: options.IncludeArchived, Limit: options.Limit, MaxTokens: options.Budget.MaxTokens,
	})
	if err != nil {
		return nil, err
	}
	result := make([]memory.ArchiveHit, 0, len(hits))
	for _, hit := range hits {
		result = append(result, memory.ArchiveHit{
			MessageID: hit.Message.ID, ConversationID: hit.ConversationID,
			Role: hit.Message.Role, Content: hit.Message.Content, CreatedAt: hit.Message.CreatedAt,
			Score: hit.Score, Snippet: strings.TrimSpace(hit.Snippet),
		})
	}
	return result, nil
}

var _ memory.Store = sqliteMemoryStore{}
var _ memory.LexicalSearcher = sqliteMemoryStore{}
var _ memory.ArchiveSearcher = sqliteMemoryStore{}
