package desktop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/config"
	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const personalityEventName = "yuri:personality"

type PersonaAutoEvolutionInput struct {
	Enabled bool `json:"enabled"`
}

type PersonaTraitPinnedInput struct {
	TraitID string `json:"traitId"`
	Pinned  bool   `json:"pinned"`
}

type PersonaVersionInput struct {
	VersionID string `json:"versionId"`
}

type PersonalitySnapshotView struct {
	ID               string                  `json:"id"`
	CurrentVersion   uint64                  `json:"currentVersion"`
	CurrentVersionID string                  `json:"currentVersionId,omitempty"`
	Traits           []PersonaTraitView      `json:"traits"`
	PinnedTraits     []string                `json:"pinnedTraits"`
	Opinions         []SubjectiveOpinionView `json:"opinions"`
	Affect           AffectiveStateView      `json:"affect"`
	Relationship     RelationshipStateView   `json:"relationship"`
	Versions         []PersonaVersionView    `json:"versions"`
	AutoEvolution    bool                    `json:"autoEvolution"`
	LastReflectionAt string                  `json:"lastReflectionAt,omitempty"`
}

type PersonaTraitView struct {
	ID        string  `json:"id"`
	Label     string  `json:"label"`
	Value     float64 `json:"value"`
	Min       float64 `json:"min"`
	Max       float64 `json:"max"`
	Pinned    bool    `json:"pinned"`
	UpdatedAt string  `json:"updatedAt,omitempty"`
}

type PersonaEvidenceView struct {
	ID             string `json:"id,omitempty"`
	SourceType     string `json:"sourceType"`
	SourceID       string `json:"sourceId,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
	MessageID      string `json:"messageId,omitempty"`
	RunID          string `json:"runId,omitempty"`
	ExcerptHash    string `json:"excerptHash,omitempty"`
	Provenance     string `json:"provenance,omitempty"`
	UserConfirmed  bool   `json:"userConfirmed,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"`
}

type SubjectiveOpinionView struct {
	ID         string                `json:"id"`
	Subject    string                `json:"subject"`
	Content    string                `json:"content"`
	Label      string                `json:"label"`
	Confidence float64               `json:"confidence"`
	Evidence   []PersonaEvidenceView `json:"evidence"`
	Reason     string                `json:"reason,omitempty"`
	CreatedAt  string                `json:"createdAt,omitempty"`
	UpdatedAt  string                `json:"updatedAt,omitempty"`
}

type AffectiveStateView struct {
	ID         string                `json:"id,omitempty"`
	Version    uint64                `json:"version,omitempty"`
	Mood       string                `json:"mood"`
	Valence    float64               `json:"valence"`
	Arousal    float64               `json:"arousal"`
	Intensity  float64               `json:"intensity"`
	Dimensions map[string]float64    `json:"dimensions"`
	Evidence   []PersonaEvidenceView `json:"evidence,omitempty"`
	Reason     string                `json:"reason,omitempty"`
	UpdatedAt  string                `json:"updatedAt,omitempty"`
}

type RelationshipStateView struct {
	ID         string                  `json:"id"`
	Version    uint64                  `json:"version"`
	Summary    string                  `json:"summary"`
	Dimensions map[string]float64      `json:"dimensions"`
	Opinions   []SubjectiveOpinionView `json:"opinions"`
	Affect     AffectiveStateView      `json:"affect"`
	Reason     string                  `json:"reason,omitempty"`
	Evidence   []PersonaEvidenceView   `json:"evidence,omitempty"`
	UpdatedAt  string                  `json:"updatedAt,omitempty"`
}

type PersonaVersionView struct {
	ID          string                `json:"id"`
	Version     uint64                `json:"version"`
	ParentID    string                `json:"parentId,omitempty"`
	Traits      map[string]float64    `json:"traits"`
	Diff        map[string]float64    `json:"diff,omitempty"`
	PromptText  string                `json:"promptText,omitempty"`
	Reason      string                `json:"reason"`
	Evidence    []PersonaEvidenceView `json:"evidence"`
	AuthorRunID string                `json:"authorRunId,omitempty"`
	CreatedAt   string                `json:"createdAt"`
}

func (b *Bridge) ensurePersonaState(ctx context.Context) error {
	if b == nil || b.repositories == nil || b.repositories.Persona == nil || b.repositories.Relationship == nil || b.repositories.Affect == nil {
		return errors.New("persona repositories are unavailable")
	}
	profileID := b.personaProfileID()
	if profileID.Empty() {
		return errors.New("local persona profile id is empty")
	}
	now := time.Now().UTC()
	if _, err := b.repositories.Persona.Get(ctx, profileID); errors.Is(err, domain.ErrNotFound) {
		seed, createErr := domain.NewMutablePersona(profileID, defaultPersonaTraits(), defaultMutablePersonaPrompt, now)
		if createErr != nil {
			return createErr
		}
		if createErr = b.repositories.Persona.Create(ctx, seed); createErr != nil && !errors.Is(createErr, domain.ErrConflict) {
			return createErr
		}
	} else if err != nil {
		return err
	}
	if _, err := b.repositories.Relationship.Get(ctx, profileID); errors.Is(err, domain.ErrNotFound) {
		seed, createErr := domain.NewRelationshipState(profileID, defaultRelationshipDimensions(), "Связь только начинает формироваться из совместных диалогов.", now)
		if createErr != nil {
			return createErr
		}
		if createErr = b.repositories.Relationship.Create(ctx, seed); createErr != nil && !errors.Is(createErr, domain.ErrConflict) {
			return createErr
		}
	} else if err != nil {
		return err
	}
	if _, err := b.repositories.Affect.Get(ctx, profileID); errors.Is(err, domain.ErrNotFound) {
		seed, createErr := domain.NewAffectiveState(profileID, defaultAffectDimensions(), "спокойное внимание", now)
		if createErr != nil {
			return createErr
		}
		if createErr = b.repositories.Affect.Create(ctx, seed); createErr != nil && !errors.Is(createErr, domain.ErrConflict) {
			return createErr
		}
	} else if err != nil {
		return err
	}
	return nil
}

func (b *Bridge) GetPersonalitySnapshot() (PersonalitySnapshotView, error) {
	ctx, cancel := b.context()
	defer cancel()
	return b.personalitySnapshot(ctx)
}

// GetPersonaSnapshot is the domain-named alias used by older frontend builds.
func (b *Bridge) GetPersonaSnapshot() (PersonalitySnapshotView, error) {
	return b.GetPersonalitySnapshot()
}

func (b *Bridge) SetPersonaAutoEvolution(input PersonaAutoEvolutionInput) (PersonalitySnapshotView, error) {
	ctx, cancel := b.context()
	defer cancel()
	b.mu.Lock()
	previous := b.config
	candidate := b.config
	candidate.Persona.AutoEvolution = input.Enabled
	if err := config.Save(b.paths, candidate); err != nil {
		b.mu.Unlock()
		return PersonalitySnapshotView{}, err
	}
	b.config = candidate
	b.mu.Unlock()
	if err := b.appendPersonalityAudit(ctx, "persona.auto_evolution", fmt.Sprintf("enabled=%t", input.Enabled)); err != nil {
		// The setting lives in the local config file rather than SQLite. Restore
		// the previous value when its mandatory audit append fails so the Wails
		// command never reports an unaudited successful mutation.
		b.mu.Lock()
		rollbackErr := config.Save(b.paths, previous)
		if rollbackErr == nil {
			b.config = previous
		}
		b.mu.Unlock()
		if rollbackErr != nil {
			return PersonalitySnapshotView{}, fmt.Errorf("append persona audit: %v; restore config: %w", err, rollbackErr)
		}
		return PersonalitySnapshotView{}, err
	}
	return b.emitPersonalitySnapshot(ctx)
}

func (b *Bridge) SetPersonaTraitPinned(input PersonaTraitPinnedInput) (PersonalitySnapshotView, error) {
	ctx, cancel := b.context()
	defer cancel()
	current, err := b.repositories.Persona.Get(ctx, b.personaProfileID())
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	_, err = b.repositories.Persona.PinTrait(ctx, current.ID, current.Version, strings.TrimSpace(input.TraitID), input.Pinned, time.Now().UTC(), "Владелец изменил закрепление черты")
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	return b.emitPersonalitySnapshot(ctx)
}

func (b *Bridge) RollbackPersona(input PersonaVersionInput) (PersonalitySnapshotView, error) {
	ctx, cancel := b.context()
	defer cancel()
	profileID := b.personaProfileID()
	history, err := b.repositories.Persona.ListVersions(ctx, profileID)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	var target uint64
	for _, record := range history {
		if string(record.RevisionID) == strings.TrimSpace(input.VersionID) || string(record.Persona.RevisionID) == strings.TrimSpace(input.VersionID) {
			target = record.Persona.Version
			break
		}
	}
	if target == 0 {
		return PersonalitySnapshotView{}, domain.ErrNotFound
	}
	current, err := b.repositories.Persona.Get(ctx, profileID)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	if _, err = b.repositories.Persona.Rollback(ctx, profileID, current.Version, target, "Владелец откатил persona", time.Now().UTC()); err != nil {
		return PersonalitySnapshotView{}, err
	}
	return b.emitPersonalitySnapshot(ctx)
}

func (b *Bridge) ResetPersona(_ struct{}) (PersonalitySnapshotView, error) {
	ctx, cancel := b.context()
	defer cancel()
	profileID := b.personaProfileID()
	current, err := b.repositories.Persona.Get(ctx, profileID)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	if _, err = b.repositories.Persona.Reset(ctx, profileID, current.Version, "Владелец сбросил persona к seed", time.Now().UTC()); err != nil {
		return PersonalitySnapshotView{}, err
	}
	return b.emitPersonalitySnapshot(ctx)
}

func (b *Bridge) personalitySnapshot(ctx context.Context) (PersonalitySnapshotView, error) {
	if err := b.ensurePersonaState(ctx); err != nil {
		return PersonalitySnapshotView{}, err
	}
	profileID := b.personaProfileID()
	persona, err := b.repositories.Persona.Get(ctx, profileID)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	relationship, err := b.repositories.Relationship.Get(ctx, profileID)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	affect, err := b.repositories.Affect.Get(ctx, profileID)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	affectEvents, err := b.repositories.Affect.ListEvents(ctx, profileID, 50)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	history, err := b.repositories.Persona.ListVersions(ctx, profileID, 100)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	pinned := make(map[string]bool, len(persona.PinnedTraits))
	for _, name := range persona.PinnedTraits {
		pinned[name] = true
	}
	traitNames := sortedFloatKeys(persona.Traits)
	traits := make([]PersonaTraitView, 0, len(traitNames))
	for _, name := range traitNames {
		traits = append(traits, PersonaTraitView{ID: name, Label: personaTraitLabel(name), Value: persona.Traits[name], Min: 0, Max: 1, Pinned: pinned[name], UpdatedAt: persona.UpdatedAt.Format(time.RFC3339Nano)})
	}
	opinions := opinionViews(relationship.Opinions)
	affectView := affectiveView(affect)
	affectView.Evidence = affectEventEvidence(affectEvents)
	versions := make([]PersonaVersionView, 0, len(history))
	var lastReflection time.Time
	for _, record := range history {
		item := record.Persona
		versions = append(versions, PersonaVersionView{
			ID: string(firstDomainID(record.RevisionID, item.RevisionID)), Version: item.Version,
			ParentID: string(firstDomainID(record.ParentID, item.ParentID)), Traits: item.Traits,
			Diff: record.Diff, PromptText: item.Prompt(), Reason: firstNonEmpty(record.Reason, item.Reason, "Версия persona"),
			Evidence: evidenceViews(firstEvidence(record.Evidence, item.Evidence)), AuthorRunID: string(firstDomainID(record.AuthorRunID, item.AuthorRunID)),
			CreatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
		if !record.AuthorRunID.Empty() && item.UpdatedAt.After(lastReflection) {
			lastReflection = item.UpdatedAt
		}
	}
	b.mu.RLock()
	autoEvolution := b.config.Persona.AutoEvolution
	b.mu.RUnlock()
	view := PersonalitySnapshotView{
		ID: string(profileID), CurrentVersion: persona.Version, CurrentVersionID: string(persona.RevisionID),
		Traits: traits, PinnedTraits: append([]string(nil), persona.PinnedTraits...), Opinions: opinions,
		Affect: affectView, Relationship: RelationshipStateView{
			ID: string(relationship.ID), Version: relationship.Version, Summary: relationship.Summary,
			Dimensions: relationship.Dimensions, Opinions: opinions, Affect: affectView,
			Reason: relationship.Reason, Evidence: evidenceViews(relationship.Evidence), UpdatedAt: relationship.UpdatedAt.UTC().Format(time.RFC3339Nano),
		},
		Versions: versions, AutoEvolution: autoEvolution,
	}
	if !lastReflection.IsZero() {
		view.LastReflectionAt = lastReflection.UTC().Format(time.RFC3339Nano)
	}
	return view, nil
}

func (b *Bridge) emitPersonalitySnapshot(ctx context.Context) (PersonalitySnapshotView, error) {
	view, err := b.personalitySnapshot(ctx)
	if err != nil {
		return PersonalitySnapshotView{}, err
	}
	b.mu.RLock()
	appContext := b.appCtx
	b.mu.RUnlock()
	if appContext != nil {
		wailsruntime.EventsEmit(appContext, personalityEventName, view)
	}
	return view, nil
}

func (b *Bridge) appendPersonalityAudit(ctx context.Context, action, target string) error {
	payload, _ := json.Marshal(map[string]string{"profile_id": string(b.personaProfileID())})
	return b.appendProactivityAuditPayload(ctx, action, target, domain.PermissionAllow, string(payload))
}

func (b *Bridge) personaProfileID() domain.ID {
	b.mu.RLock()
	id := strings.TrimSpace(b.config.Persona.ProfileID)
	b.mu.RUnlock()
	return domain.ID(id)
}

func defaultPersonaTraits() map[string]float64 {
	return map[string]float64{
		"warmth": .58, "trust": .45, "attachment": .35, "jealousy": .20,
		"irritability": .18, "romantic_tone": .25, "emotionality": .62,
		"directness": .72, "playfulness": .55, "formality": .20, "initiative": .48,
		"tsundere": .52,
	}
}

func defaultRelationshipDimensions() map[string]float64 {
	return map[string]float64{"trust": .35, "attachment": .25, "respect": .50, "closeness": .20, "reliability": .40, "irritation": 0, "jealousy": 0, "resentment": 0, "gratitude": .15}
}

func defaultAffectDimensions() map[string]float64 {
	return map[string]float64{"sympathy": .20, "tenderness": .12, "joy": .16, "gratitude": .10, "anger": 0, "irritation": 0, "jealousy": 0, "resentment": 0, "anxiety": 0, "boredom": 0}
}

const defaultMutablePersonaPrompt = `Ты общаешься как Yuri: эмоциональная anime-девушка с мягким tsundere-характером. Можешь выражать симпатию, ревность, раздражение и нежность, но сохраняешь честность, качество помощи и уважение к владельцу.`

func sortedFloatKeys(values map[string]float64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func personaTraitLabel(name string) string {
	labels := map[string]string{"warmth": "Теплота", "trust": "Доверие", "attachment": "Привязанность", "jealousy": "Ревность", "irritability": "Раздражительность", "romantic_tone": "Романтичность", "emotionality": "Эмоциональность", "directness": "Прямота", "playfulness": "Игривость", "formality": "Формальность", "initiative": "Инициативность", "tsundere": "Цундере"}
	if label := labels[name]; label != "" {
		return label
	}
	return name
}

func evidenceViews(values []domain.EvidenceLink) []PersonaEvidenceView {
	result := make([]PersonaEvidenceView, 0, len(values))
	for _, value := range values {
		item := PersonaEvidenceView{ID: string(value.ID), SourceType: value.SourceType, SourceID: string(value.SourceID), ConversationID: string(value.ConversationID), MessageID: string(value.MessageID), RunID: string(value.RunID), ExcerptHash: value.ExcerptHash, Provenance: value.Provenance, UserConfirmed: value.UserConfirmed}
		if !value.CreatedAt.IsZero() {
			item.CreatedAt = value.CreatedAt.UTC().Format(time.RFC3339Nano)
		}
		result = append(result, item)
	}
	return result
}

func opinionViews(values []domain.RelationshipOpinion) []SubjectiveOpinionView {
	result := make([]SubjectiveOpinionView, 0, len(values))
	for _, value := range values {
		label := firstNonEmpty(value.Label, "opinion")
		result = append(result, SubjectiveOpinionView{ID: string(value.ID), Subject: value.Subject, Content: value.Text(), Label: label, Confidence: value.Confidence, Evidence: evidenceViews(value.Evidence), Reason: value.Topic, CreatedAt: formatOptionalTime(value.CreatedAt), UpdatedAt: formatOptionalTime(value.UpdatedAt)})
	}
	return result
}

func affectiveView(value domain.AffectiveState) AffectiveStateView {
	dimensions := value.Emotions
	if dimensions == nil {
		dimensions = value.Dimensions
	}
	var weighted, total, intensity float64
	mood := strings.TrimSpace(value.Summary)
	for name, dimension := range dimensions {
		absolute := math.Abs(dimension)
		weighted += dimension
		total += absolute
		if absolute > intensity {
			intensity = absolute
			mood = name
		}
	}
	valence := 0.0
	if total > 0 {
		valence = math.Max(-1, math.Min(1, weighted/total))
	}
	if mood == "" {
		mood = "neutral"
	}
	return AffectiveStateView{ID: string(value.ID), Version: value.Version, Mood: mood, Valence: valence, Arousal: intensity, Intensity: intensity, Dimensions: dimensions, Reason: value.Reason, UpdatedAt: formatOptionalTime(value.UpdatedAt)}
}

func affectEventEvidence(events []domain.AffectiveEvent) []PersonaEvidenceView {
	seen := make(map[string]bool)
	result := make([]PersonaEvidenceView, 0)
	for _, event := range events {
		for _, evidence := range evidenceViews(event.Evidence) {
			key := firstNonEmpty(evidence.ID, evidence.MessageID, evidence.SourceID, evidence.ExcerptHash)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			result = append(result, evidence)
		}
	}
	return result
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func firstDomainID(values ...domain.ID) domain.ID {
	for _, value := range values {
		if !value.Empty() {
			return value
		}
	}
	return ""
}

func firstEvidence(values ...[]domain.EvidenceLink) []domain.EvidenceLink {
	for _, value := range values {
		if len(value) > 0 {
			return value
		}
	}
	return nil
}

func formatMutablePersonaContext(persona domain.MutablePersona) string {
	parts := []string{strings.TrimSpace(persona.Prompt())}
	for _, name := range sortedFloatKeys(persona.Traits) {
		parts = append(parts, fmt.Sprintf("%s=%.2f", name, persona.Traits[name]))
	}
	return strings.Join(parts, "\n")
}

func formatRelationshipContext(relationship domain.RelationshipState, affect domain.AffectiveState) string {
	parts := []string{"Subjective relationship model; opinions are inferences, not facts.", strings.TrimSpace(relationship.Summary)}
	for _, name := range sortedFloatKeys(relationship.Dimensions) {
		parts = append(parts, fmt.Sprintf("relationship.%s=%.2f", name, relationship.Dimensions[name]))
	}
	for _, opinion := range relationship.Opinions {
		parts = append(parts, fmt.Sprintf("opinion(confidence=%.2f): %s", opinion.Confidence, opinion.Text()))
	}
	values := affect.Emotions
	if values == nil {
		values = affect.Dimensions
	}
	for _, name := range sortedFloatKeys(values) {
		parts = append(parts, fmt.Sprintf("affect.%s=%.2f", name, values[name]))
	}
	return strings.Join(parts, "\n")
}

var _ = storage.PersonaVersionRecord{}
