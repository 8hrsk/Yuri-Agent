package desktop

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	portableProfileFormat   = "yuri.agent-profile"
	portableProfileVersion  = 1
	portableProfileMaxBytes = 1024 * 1024
)

type PortableAgentProfilePathInput struct {
	Path string `json:"path,omitempty"`
}

type PortableAgentProfileView struct {
	Path       string           `json:"path"`
	ExportedAt string           `json:"exportedAt"`
	SizeBytes  int64            `json:"sizeBytes"`
	Checksum   string           `json:"checksum"`
	Profile    CreateAgentInput `json:"profile"`
}

type portableAgentProfileEnvelope struct {
	Format     string           `json:"format"`
	Version    int              `json:"version"`
	ExportedAt time.Time        `json:"exported_at"`
	Checksum   string           `json:"profile_sha256"`
	Profile    CreateAgentInput `json:"profile"`
}

// ExportActiveAgentProfile writes only the owner-authored creation contract.
// Runtime histories, memories, credentials, permissions, plugin grants and
// local IDs are not members of the envelope and therefore cannot leak into it.
func (b *Bridge) ExportActiveAgentProfile(input PortableAgentProfilePathInput) (PortableAgentProfileView, error) {
	ctx, cancel := b.portableProfileContext()
	defer cancel()
	agentID := b.personaProfileID()
	profile, err := b.repositories.Agents.Get(ctx, agentID)
	if err != nil {
		return PortableAgentProfileView{}, err
	}
	seed, err := b.repositories.Personalization.Get(ctx, agentID)
	if err != nil {
		return PortableAgentProfileView{}, err
	}
	portable := portableCreateAgentInput(profile, seed)
	exportedAt := time.Now().UTC()
	envelope, data, err := encodePortableAgentProfile(portable, exportedAt)
	if err != nil {
		return PortableAgentProfileView{}, err
	}
	path, err := b.resolvePortableProfileDestination(ctx, input.Path)
	if err != nil {
		return PortableAgentProfileView{}, err
	}
	if err := writePortableProfile(path, data); err != nil {
		return PortableAgentProfileView{}, err
	}
	b.recordPortableProfileAudit(ctx, "personalization.profile.export", profile.ID, path, envelope.Checksum)
	return PortableAgentProfileView{Path: path, ExportedAt: exportedAt.Format(time.RFC3339Nano), SizeBytes: int64(len(data)), Checksum: envelope.Checksum, Profile: portable}, nil
}

// OpenPortableAgentProfile validates a portable file without creating or
// activating anything. The UI must present Profile through the normal creation
// review before calling CreateAgent.
func (b *Bridge) OpenPortableAgentProfile(input PortableAgentProfilePathInput) (PortableAgentProfileView, error) {
	ctx, cancel := b.portableProfileContext()
	defer cancel()
	path, err := b.resolvePortableProfileSource(ctx, input.Path)
	if err != nil {
		return PortableAgentProfileView{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return PortableAgentProfileView{}, fmt.Errorf("открыть portable profile: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, portableProfileMaxBytes+1))
	if err != nil {
		return PortableAgentProfileView{}, fmt.Errorf("прочитать portable profile: %w", err)
	}
	if len(data) > portableProfileMaxBytes {
		return PortableAgentProfileView{}, fmt.Errorf("%w: portable profile exceeds 1 MiB", domain.ErrInvalidArgument)
	}
	envelope, err := decodePortableAgentProfile(data)
	if err != nil {
		return PortableAgentProfileView{}, err
	}
	// Reuse the production creation boundary for semantic validation while
	// deliberately performing no repository writes.
	if _, err := buildAgentCreationState("portable-profile-validation", envelope.Profile, time.Now().UTC()); err != nil {
		return PortableAgentProfileView{}, fmt.Errorf("некорректный профиль агента: %w", err)
	}
	b.recordPortableProfileAudit(ctx, "personalization.profile.inspect", "", path, envelope.Checksum)
	return PortableAgentProfileView{Path: path, ExportedAt: envelope.ExportedAt.Format(time.RFC3339Nano), SizeBytes: int64(len(data)), Checksum: envelope.Checksum, Profile: envelope.Profile}, nil
}

func (b *Bridge) portableProfileContext() (context.Context, context.CancelFunc) {
	b.mu.RLock()
	appContext := b.appCtx
	b.mu.RUnlock()
	if appContext == nil {
		appContext = context.Background()
	}
	return context.WithTimeout(appContext, 10*time.Minute)
}

func encodePortableAgentProfile(profile CreateAgentInput, exportedAt time.Time) (portableAgentProfileEnvelope, []byte, error) {
	payload, err := json.Marshal(profile)
	if err != nil {
		return portableAgentProfileEnvelope{}, nil, err
	}
	if looksLikeSecret(string(payload)) {
		return portableAgentProfileEnvelope{}, nil, fmt.Errorf("%w: owner profile contains secret-like material", domain.ErrNotPermitted)
	}
	digest := sha256.Sum256(payload)
	envelope := portableAgentProfileEnvelope{Format: portableProfileFormat, Version: portableProfileVersion, ExportedAt: exportedAt.UTC(), Checksum: "sha256:" + hex.EncodeToString(digest[:]), Profile: profile}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return portableAgentProfileEnvelope{}, nil, err
	}
	data = append(data, '\n')
	if len(data) > portableProfileMaxBytes {
		return portableAgentProfileEnvelope{}, nil, fmt.Errorf("%w: portable profile exceeds 1 MiB", domain.ErrInvalidArgument)
	}
	return envelope, data, nil
}

func decodePortableAgentProfile(data []byte) (portableAgentProfileEnvelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var envelope portableAgentProfileEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return portableAgentProfileEnvelope{}, fmt.Errorf("прочитать portable profile JSON: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return portableAgentProfileEnvelope{}, fmt.Errorf("%w: trailing portable profile data", domain.ErrInvalidArgument)
	}
	if envelope.Format != portableProfileFormat || envelope.Version != portableProfileVersion || envelope.ExportedAt.IsZero() || envelope.Profile.Personalization == nil {
		return portableAgentProfileEnvelope{}, fmt.Errorf("%w: unsupported or incomplete portable profile", domain.ErrInvalidArgument)
	}
	payload, err := json.Marshal(envelope.Profile)
	if err != nil {
		return portableAgentProfileEnvelope{}, err
	}
	if looksLikeSecret(string(payload)) {
		return portableAgentProfileEnvelope{}, fmt.Errorf("%w: portable profile contains secret-like material", domain.ErrNotPermitted)
	}
	digest := sha256.Sum256(payload)
	want := "sha256:" + hex.EncodeToString(digest[:])
	if !strings.EqualFold(envelope.Checksum, want) {
		return portableAgentProfileEnvelope{}, fmt.Errorf("%w: portable profile checksum mismatch", domain.ErrInvalidArgument)
	}
	return envelope, nil
}

func writePortableProfile(path string, data []byte) (err error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("создать директорию portable profile: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".yuri-profile-*.tmp")
	if err != nil {
		return fmt.Errorf("создать временный portable profile: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("записать portable profile: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("сохранить portable profile: %w", err)
	}
	return nil
}

func (b *Bridge) resolvePortableProfileDestination(ctx context.Context, rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		if !b.hasAppContext() {
			return "", errors.New("путь portable profile обязателен вне Wails UI")
		}
		var err error
		path, err = wailsruntime.SaveFileDialog(ctx, wailsruntime.SaveDialogOptions{Title: "Экспортировать профиль агента", DefaultFilename: "yuri-agent-profile.json", CanCreateDirectories: true, Filters: []wailsruntime.FileFilter{{DisplayName: "Yuri agent profile (*.json)", Pattern: "*.json"}}})
		if err != nil {
			return "", fmt.Errorf("выбрать файл экспорта: %w", err)
		}
		if path == "" {
			return "", errors.New("экспорт профиля отменён")
		}
	}
	return filepath.Abs(path)
}

func (b *Bridge) resolvePortableProfileSource(ctx context.Context, rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		if !b.hasAppContext() {
			return "", errors.New("путь portable profile обязателен вне Wails UI")
		}
		var err error
		path, err = wailsruntime.OpenFileDialog(ctx, wailsruntime.OpenDialogOptions{Title: "Импортировать профиль агента", Filters: []wailsruntime.FileFilter{{DisplayName: "Yuri agent profile (*.json)", Pattern: "*.json"}}})
		if err != nil {
			return "", fmt.Errorf("выбрать portable profile: %w", err)
		}
		if path == "" {
			return "", errors.New("импорт профиля отменён")
		}
	}
	return filepath.Abs(path)
}

func (b *Bridge) recordPortableProfileAudit(ctx context.Context, action string, agentID domain.ID, path, checksum string) {
	if b.repositories == nil || b.repositories.Audit == nil {
		return
	}
	id, err := domain.NewID("audit")
	if err != nil {
		return
	}
	payload, _ := json.Marshal(map[string]any{"file_name": filepath.Base(path), "format": portableProfileFormat, "version": portableProfileVersion, "checksum": checksum, "contains_runtime_history": false, "contains_permissions": false, "contains_secrets": false})
	target := agentID.String()
	if target == "" {
		target = filepath.Base(path)
	}
	if err := b.repositories.Audit.Append(ctx, storage.AuditEvent{ID: id, Actor: domain.ActorUser, Action: action, Target: target, Decision: domain.PermissionAllow, PayloadRedacted: string(payload), CreatedAt: time.Now().UTC()}); err != nil && b.logger != nil {
		b.logger.ErrorContext(ctx, "append portable profile audit", "action", action, "error", err)
	}
}

func portableCreateAgentInput(profile domain.AgentProfile, seed domain.PersonalizationSeed) CreateAgentInput {
	bounds := make(map[string]CreateAgentNumericRangeInput, len(seed.EvolutionPolicy.TraitBounds))
	for name, value := range seed.EvolutionPolicy.TraitBounds {
		bounds[name] = CreateAgentNumericRangeInput{Min: value.Min, Max: value.Max}
	}
	episodes := make([]CreateAgentBackstoryEpisodeInput, 0, len(seed.Backstory.Episodes))
	for _, episode := range seed.Backstory.Episodes {
		episodes = append(episodes, CreateAgentBackstoryEpisodeInput{ID: episode.ID, Title: episode.Title, Content: episode.Content, Kind: episode.Kind, People: append([]string(nil), episode.People...), Place: episode.Place, EmotionalValence: episode.EmotionalValence, Sequence: episode.Sequence})
	}
	return CreateAgentInput{Name: profile.Name, Age: profile.Age, Gender: profile.Gender, Preferences: seed.Identity.SelfDescription, Backstory: seed.Backstory.Narrative,
		ProviderID: profile.ProviderID, Model: profile.Model, ExecutionBudget: string(profile.ExecutionBudget.Normalized()), Traits: seed.Temperament.Traits(), Personalization: &CreateAgentPersonalizationInput{
			Identity:            CreateAgentIdentityInput{PreferredLanguage: seed.Identity.PreferredLanguage, Pronouns: seed.Identity.Pronouns, UserAddress: seed.Identity.UserAddress, SelfDescription: seed.Identity.SelfDescription, Role: seed.Identity.Role},
			CommunicationStyle:  CreateAgentCommunicationStyleInput{Verbosity: seed.CommunicationStyle.Verbosity, Softness: seed.CommunicationStyle.Softness, Humor: seed.CommunicationStyle.Humor, Figurativeness: seed.CommunicationStyle.Figurativeness, Expressiveness: seed.CommunicationStyle.Expressiveness, Supportiveness: seed.CommunicationStyle.Supportiveness, Formality: seed.CommunicationStyle.Formality, Teasing: seed.CommunicationStyle.Teasing, EmojiFrequency: seed.CommunicationStyle.EmojiFrequency, Flirtation: seed.CommunicationStyle.Flirtation, ConversationalInitiative: seed.CommunicationStyle.ConversationalInitiative},
			EmotionalDynamics:   CreateAgentEmotionalDynamicsInput{Reactivity: seed.EmotionalDynamics.Reactivity, ResponseIntensity: seed.EmotionalDynamics.ResponseIntensity, RecoverySpeed: seed.EmotionalDynamics.RecoverySpeed, PositivePersistence: seed.EmotionalDynamics.PositivePersistence, NegativePersistence: seed.EmotionalDynamics.NegativePersistence, Expression: seed.EmotionalDynamics.Expression, Masking: seed.EmotionalDynamics.Masking, ConflictStyle: seed.EmotionalDynamics.ConflictStyle, Triggers: seed.EmotionalDynamics.Triggers, SoothingStrategies: append([]string(nil), seed.EmotionalDynamics.SoothingStrategies...)},
			RelationshipSeed:    CreateAgentRelationshipSeedInput{Preset: string(seed.RelationshipSeed.Preset), Dimensions: seed.RelationshipSeed.Dimensions, Summary: seed.RelationshipSeed.Summary},
			StructuredBackstory: CreateAgentStructuredBackstoryInput{Narrative: seed.Backstory.Narrative, Summary: seed.Backstory.Summary, Episodes: episodes},
			EvolutionPolicy:     CreateAgentEvolutionPolicyInput{LockedFields: append([]string(nil), seed.EvolutionPolicy.LockedFields...), TraitBounds: bounds, ReflectionMode: string(seed.EvolutionPolicy.ReflectionMode), ReflectionCooldownMinutes: seed.EvolutionPolicy.ReflectionCooldownMinutes, ReflectionMaxTokens: seed.EvolutionPolicy.ReflectionMaxTokens, ReflectionMaxDurationSecs: seed.EvolutionPolicy.ReflectionMaxDurationSecs, ReflectionMaxEvidence: seed.EvolutionPolicy.ReflectionMaxEvidence},
		}}
}
