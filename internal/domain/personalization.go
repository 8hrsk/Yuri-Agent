package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	PersonalizationSchemaVersion = 2
	PersonalizationTextMaxRunes  = 2_000
	BackstorySummaryMaxRunes     = 2_000
	BackstoryEpisodeMaxRunes     = 4_000
	BackstoryEpisodeMaxCount     = 64
	PersonalizationListMaxCount  = 64
)

// PersonalizationOperation describes why an owner seed revision exists. The
// seed is never writable by reflection: update/reset are explicit owner
// operations, while migration imports an older installation losslessly.
type PersonalizationOperation string

const (
	PersonalizationOperationCreate    PersonalizationOperation = "create"
	PersonalizationOperationUpdate    PersonalizationOperation = "update"
	PersonalizationOperationReset     PersonalizationOperation = "reset"
	PersonalizationOperationMigration PersonalizationOperation = "migration"
)

func (operation PersonalizationOperation) Valid() bool {
	switch operation {
	case PersonalizationOperationCreate, PersonalizationOperationUpdate,
		PersonalizationOperationReset, PersonalizationOperationMigration:
		return true
	default:
		return false
	}
}

// IdentityPersonalization extends the top-level AgentProfile without
// duplicating its name, age, or gender. These fields are owner-authored and
// cannot be changed by normal reflection.
type IdentityPersonalization struct {
	PreferredLanguage string `json:"preferred_language,omitempty"`
	Pronouns          string `json:"pronouns,omitempty"`
	UserAddress       string `json:"user_address,omitempty"`
	SelfDescription   string `json:"self_description,omitempty"`
	Role              string `json:"role,omitempty"`
}

// CommunicationStyle contains directly observable response-shaping values.
// All values use [0,1]; the Personality Compiler owns their linguistic
// interpretation and must not treat them as permissions or tool policy.
type CommunicationStyle struct {
	Verbosity                float64 `json:"verbosity"`
	Softness                 float64 `json:"softness"`
	Humor                    float64 `json:"humor"`
	Figurativeness           float64 `json:"figurativeness"`
	Expressiveness           float64 `json:"expressiveness"`
	Supportiveness           float64 `json:"supportiveness"`
	Formality                float64 `json:"formality"`
	Teasing                  float64 `json:"teasing"`
	EmojiFrequency           float64 `json:"emoji_frequency"`
	Flirtation               float64 `json:"flirtation"`
	ConversationalInitiative float64 `json:"conversational_initiative"`
}

func (style CommunicationStyle) values() map[string]float64 {
	return map[string]float64{
		"verbosity": style.Verbosity, "softness": style.Softness, "humor": style.Humor,
		"figurativeness": style.Figurativeness, "expressiveness": style.Expressiveness,
		"supportiveness": style.Supportiveness, "formality": style.Formality,
		"teasing": style.Teasing, "emoji_frequency": style.EmojiFrequency,
		"flirtation": style.Flirtation, "conversational_initiative": style.ConversationalInitiative,
	}
}

// Temperament is the stable predisposition layer. Explicit fields make the
// product contract reviewable; Custom preserves safe legacy/extension traits
// without silently discarding them during migration.
type Temperament struct {
	Warmth             float64            `json:"warmth"`
	Directness         float64            `json:"directness"`
	Emotionality       float64            `json:"emotionality"`
	Playfulness        float64            `json:"playfulness"`
	Jealousy           float64            `json:"jealousy"`
	Irritability       float64            `json:"irritability"`
	Empathy            float64            `json:"empathy"`
	Sociability        float64            `json:"sociability"`
	Shyness            float64            `json:"shyness"`
	Anxiety            float64            `json:"anxiety"`
	Fearfulness        float64            `json:"fearfulness"`
	EmotionalStability float64            `json:"emotional_stability"`
	Sensitivity        float64            `json:"sensitivity"`
	Possessiveness     float64            `json:"possessiveness"`
	RomanticTone       float64            `json:"romantic_tone"`
	Initiative         float64            `json:"initiative"`
	Impulsivity        float64            `json:"impulsivity"`
	Stubbornness       float64            `json:"stubbornness"`
	Optimism           float64            `json:"optimism"`
	Curiosity          float64            `json:"curiosity"`
	Suspicion          float64            `json:"suspicion"`
	Trust              float64            `json:"trust"`
	Attachment         float64            `json:"attachment"`
	Formality          float64            `json:"formality"`
	Tsundere           float64            `json:"tsundere"`
	Custom             map[string]float64 `json:"custom"`
}

var standardTemperamentTraits = []string{
	"warmth", "directness", "emotionality", "playfulness", "jealousy", "irritability",
	"empathy", "sociability", "shyness", "anxiety", "fearfulness", "emotional_stability",
	"sensitivity", "possessiveness", "romantic_tone", "initiative", "impulsivity", "stubbornness",
	"optimism", "curiosity", "suspicion", "trust", "attachment", "formality", "tsundere",
}

// DefaultTemperament is the canonical owner seed used by backend creation and
// legacy migration. Frontend defaults mirror this contract.
func DefaultTemperament() Temperament {
	return Temperament{
		Warmth: .58, Directness: .72, Emotionality: .62, Playfulness: .55,
		Jealousy: .20, Irritability: .18, Empathy: .72, Sociability: .48,
		Shyness: .34, Anxiety: .22, Fearfulness: .18, EmotionalStability: .64,
		Sensitivity: .58, Possessiveness: .16, RomanticTone: .25, Initiative: .48,
		Impulsivity: .22, Stubbornness: .38, Optimism: .58, Curiosity: .72,
		Suspicion: .18, Trust: .45, Attachment: .35, Formality: .20, Tsundere: .52,
		Custom: map[string]float64{},
	}
}

func TemperamentFromTraits(traits map[string]float64) Temperament {
	result := DefaultTemperament()
	values := result.Traits()
	custom := make(map[string]float64)
	for name, value := range traits {
		name = strings.TrimSpace(name)
		if _, standard := values[name]; standard {
			values[name] = value
		} else if name != "" {
			custom[name] = value
		}
	}
	result = temperamentFromStandard(values)
	result.Custom = custom
	return result
}

func temperamentFromStandard(values map[string]float64) Temperament {
	return Temperament{
		Warmth: values["warmth"], Directness: values["directness"], Emotionality: values["emotionality"],
		Playfulness: values["playfulness"], Jealousy: values["jealousy"], Irritability: values["irritability"],
		Empathy: values["empathy"], Sociability: values["sociability"], Shyness: values["shyness"],
		Anxiety: values["anxiety"], Fearfulness: values["fearfulness"], EmotionalStability: values["emotional_stability"],
		Sensitivity: values["sensitivity"], Possessiveness: values["possessiveness"], RomanticTone: values["romantic_tone"],
		Initiative: values["initiative"], Impulsivity: values["impulsivity"], Stubbornness: values["stubbornness"],
		Optimism: values["optimism"], Curiosity: values["curiosity"], Suspicion: values["suspicion"],
		Trust: values["trust"], Attachment: values["attachment"], Formality: values["formality"], Tsundere: values["tsundere"],
	}
}

func (temperament Temperament) Traits() map[string]float64 {
	result := map[string]float64{
		"warmth": temperament.Warmth, "directness": temperament.Directness,
		"emotionality": temperament.Emotionality, "playfulness": temperament.Playfulness,
		"jealousy": temperament.Jealousy, "irritability": temperament.Irritability,
		"empathy": temperament.Empathy, "sociability": temperament.Sociability,
		"shyness": temperament.Shyness, "anxiety": temperament.Anxiety,
		"fearfulness": temperament.Fearfulness, "emotional_stability": temperament.EmotionalStability,
		"sensitivity": temperament.Sensitivity, "possessiveness": temperament.Possessiveness,
		"romantic_tone": temperament.RomanticTone, "initiative": temperament.Initiative,
		"impulsivity": temperament.Impulsivity, "stubbornness": temperament.Stubbornness,
		"optimism": temperament.Optimism, "curiosity": temperament.Curiosity,
		"suspicion": temperament.Suspicion, "trust": temperament.Trust,
		"attachment": temperament.Attachment, "formality": temperament.Formality,
		"tsundere": temperament.Tsundere,
	}
	for name, value := range temperament.Custom {
		result[name] = value
	}
	return result
}

type EmotionalDynamics struct {
	Reactivity          float64             `json:"reactivity"`
	ResponseIntensity   float64             `json:"response_intensity"`
	RecoverySpeed       float64             `json:"recovery_speed"`
	PositivePersistence float64             `json:"positive_persistence"`
	NegativePersistence float64             `json:"negative_persistence"`
	Expression          float64             `json:"expression"`
	Masking             float64             `json:"masking"`
	ConflictStyle       string              `json:"conflict_style"`
	Triggers            map[string][]string `json:"triggers,omitempty"`
	SoothingStrategies  []string            `json:"soothing_strategies,omitempty"`
}

func DefaultEmotionalDynamics(temperament Temperament) EmotionalDynamics {
	return EmotionalDynamics{
		Reactivity: temperament.Sensitivity, ResponseIntensity: temperament.Emotionality,
		RecoverySpeed: temperament.EmotionalStability, PositivePersistence: .50,
		NegativePersistence: clamp(1-temperament.EmotionalStability, 0, 1),
		Expression:          temperament.Emotionality, Masking: .25, ConflictStyle: "adaptive",
		Triggers: map[string][]string{}, SoothingStrategies: []string{},
	}
}

func (dynamics EmotionalDynamics) values() map[string]float64 {
	return map[string]float64{
		"reactivity": dynamics.Reactivity, "response_intensity": dynamics.ResponseIntensity,
		"recovery_speed": dynamics.RecoverySpeed, "positive_persistence": dynamics.PositivePersistence,
		"negative_persistence": dynamics.NegativePersistence, "expression": dynamics.Expression,
		"masking": dynamics.Masking,
	}
}

type RelationshipSeedPreset string

const (
	RelationshipSeedNew           RelationshipSeedPreset = "new_acquaintances"
	RelationshipSeedAcquaintances RelationshipSeedPreset = "acquaintances"
	RelationshipSeedFriends       RelationshipSeedPreset = "friends"
	RelationshipSeedCloseFriends  RelationshipSeedPreset = "close_friends"
	RelationshipSeedProfessional  RelationshipSeedPreset = "professional"
	RelationshipSeedRomantic      RelationshipSeedPreset = "romantic_partners"
	RelationshipSeedCustom        RelationshipSeedPreset = "custom"
)

func (preset RelationshipSeedPreset) Valid() bool {
	switch preset {
	case RelationshipSeedNew, RelationshipSeedAcquaintances, RelationshipSeedFriends,
		RelationshipSeedCloseFriends, RelationshipSeedProfessional,
		RelationshipSeedRomantic, RelationshipSeedCustom:
		return true
	default:
		return false
	}
}

type RelationshipSeed struct {
	Preset     RelationshipSeedPreset `json:"preset"`
	Dimensions map[string]float64     `json:"dimensions"`
	Summary    string                 `json:"summary,omitempty"`
}

func (seed RelationshipSeed) Validate() error {
	if !seed.Preset.Valid() {
		return fmt.Errorf("%w: invalid relationship seed preset", ErrInvalidArgument)
	}
	if len(seed.Dimensions) == 0 || len(seed.Dimensions) > DefaultPersonaLimits.MaxTraits {
		return fmt.Errorf("%w: relationship seed dimensions are required", ErrInvalidArgument)
	}
	for name, value := range seed.Dimensions {
		if !validDimensionName(name) || !finite(value) || value < 0 || value > 1 {
			return fmt.Errorf("%w: relationship seed dimension %q is invalid", ErrInvalidArgument, name)
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(seed.Summary)) > PersonalizationTextMaxRunes || strings.ContainsRune(seed.Summary, '\x00') {
		return fmt.Errorf("%w: relationship seed summary is invalid", ErrInvalidArgument)
	}
	return nil
}

type BackstoryEpisode struct {
	ID               string   `json:"id"`
	Title            string   `json:"title,omitempty"`
	Content          string   `json:"content"`
	Kind             string   `json:"kind,omitempty"`
	People           []string `json:"people,omitempty"`
	Place            string   `json:"place,omitempty"`
	EmotionalValence float64  `json:"emotional_valence,omitempty"`
	Sequence         int      `json:"sequence,omitempty"`
}

type StructuredBackstory struct {
	Narrative string             `json:"narrative,omitempty"`
	Summary   string             `json:"summary,omitempty"`
	Episodes  []BackstoryEpisode `json:"episodes,omitempty"`
}

type NumericRange struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

type PersonalizationReflectionMode string

const (
	PersonalizationReflectionEnabled  PersonalizationReflectionMode = "enabled"
	PersonalizationReflectionDisabled PersonalizationReflectionMode = "disabled"
)

type PersonalizationEvolutionPolicy struct {
	LockedFields              []string                      `json:"locked_fields,omitempty"`
	TraitBounds               map[string]NumericRange       `json:"trait_bounds,omitempty"`
	ReflectionMode            PersonalizationReflectionMode `json:"reflection_mode,omitempty"`
	ReflectionCooldownMinutes int                           `json:"reflection_cooldown_minutes,omitempty"`
	ReflectionMaxTokens       int64                         `json:"reflection_max_tokens,omitempty"`
	ReflectionMaxDurationSecs int                           `json:"reflection_max_duration_seconds,omitempty"`
	ReflectionMaxEvidence     int                           `json:"reflection_max_evidence,omitempty"`
}

func (policy PersonalizationEvolutionPolicy) FieldLocked(field string) bool {
	field = strings.TrimSpace(field)
	for _, candidate := range policy.LockedFields {
		if strings.TrimSpace(candidate) == field {
			return true
		}
	}
	return false
}

func (policy PersonalizationEvolutionPolicy) ReflectionEnabled(fallback bool) bool {
	switch policy.ReflectionMode {
	case PersonalizationReflectionEnabled:
		return true
	case PersonalizationReflectionDisabled:
		return false
	default:
		return fallback
	}
}

func (policy PersonalizationEvolutionPolicy) ReflectionCooldown(fallback time.Duration) time.Duration {
	if policy.ReflectionCooldownMinutes <= 0 {
		return fallback
	}
	return time.Duration(policy.ReflectionCooldownMinutes) * time.Minute
}

// PersonalizationSeed is the append-only owner baseline. Runtime persona,
// relationship and affect states reference its semantics but remain separate
// journals so background reflection can never rewrite the baseline.
type PersonalizationSeed struct {
	AgentID            ID                             `json:"agent_id"`
	SchemaVersion      int                            `json:"schema_version"`
	Version            uint64                         `json:"version"`
	RevisionID         ID                             `json:"revision_id"`
	ParentID           ID                             `json:"parent_id,omitempty"`
	ParentVersion      uint64                         `json:"parent_version,omitempty"`
	Operation          PersonalizationOperation       `json:"operation"`
	Identity           IdentityPersonalization        `json:"identity"`
	CommunicationStyle CommunicationStyle             `json:"communication_style"`
	Temperament        Temperament                    `json:"temperament"`
	EmotionalDynamics  EmotionalDynamics              `json:"emotional_dynamics"`
	RelationshipSeed   RelationshipSeed               `json:"relationship_seed"`
	Backstory          StructuredBackstory            `json:"backstory"`
	EvolutionPolicy    PersonalizationEvolutionPolicy `json:"evolution_policy"`
	Reason             string                         `json:"reason,omitempty"`
	CreatedAt          time.Time                      `json:"created_at"`
	UpdatedAt          time.Time                      `json:"updated_at"`
}

func NewPersonalizationSeed(profile AgentProfile, traits, relationship map[string]float64, now time.Time) (PersonalizationSeed, error) {
	if err := profile.Validate(); err != nil {
		return PersonalizationSeed{}, err
	}
	temperament := TemperamentFromTraits(traits)
	if len(relationship) == 0 {
		relationship = map[string]float64{
			"trust": .35, "attachment": .25, "respect": .50, "closeness": .20,
			"reliability": .40, "irritation": 0, "jealousy": 0, "resentment": 0, "gratitude": .15,
		}
	}
	seed := PersonalizationSeed{
		AgentID: profile.ID, SchemaVersion: PersonalizationSchemaVersion, Version: 1,
		RevisionID: ID(fmt.Sprintf("%s:personalization:v1", profile.ID)), Operation: PersonalizationOperationCreate,
		Identity: IdentityPersonalization{PreferredLanguage: "ru-RU", SelfDescription: profile.Preferences},
		CommunicationStyle: CommunicationStyle{
			Verbosity: .55, Softness: temperament.Warmth, Humor: temperament.Playfulness,
			Figurativeness: .35, Expressiveness: temperament.Emotionality,
			Supportiveness: temperament.Empathy, Formality: temperament.Formality,
			Teasing: temperament.Tsundere, EmojiFrequency: .10,
			Flirtation: temperament.RomanticTone, ConversationalInitiative: temperament.Initiative,
		},
		Temperament: temperament, EmotionalDynamics: DefaultEmotionalDynamics(temperament),
		RelationshipSeed: RelationshipSeed{Preset: RelationshipSeedNew, Dimensions: cloneFloatMap(relationship), Summary: "The bond is only beginning to form."},
		Backstory:        StructuredBackstory{Narrative: profile.Backstory},
		EvolutionPolicy:  defaultPersonalizationEvolutionPolicy(temperament),
		Reason:           "owner personalization seed", CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := seed.Validate(); err != nil {
		return PersonalizationSeed{}, err
	}
	return seed, nil
}

func defaultPersonalizationEvolutionPolicy(temperament Temperament) PersonalizationEvolutionPolicy {
	bounds := make(map[string]NumericRange, len(temperament.Traits()))
	for name := range temperament.Traits() {
		bounds[name] = NumericRange{Min: 0, Max: 1}
	}
	return PersonalizationEvolutionPolicy{
		LockedFields: []string{"identity", "backstory"}, TraitBounds: bounds,
		ReflectionMode: PersonalizationReflectionEnabled, ReflectionCooldownMinutes: 60,
		ReflectionMaxTokens: 2_500, ReflectionMaxDurationSecs: 60, ReflectionMaxEvidence: 8,
	}
}

func (seed PersonalizationSeed) Validate() error {
	if seed.AgentID.Empty() || seed.SchemaVersion != PersonalizationSchemaVersion || seed.Version == 0 || seed.RevisionID.Empty() {
		return fmt.Errorf("%w: personalization identity, schema, version and revision are required", ErrInvalidArgument)
	}
	if !seed.Operation.Valid() {
		return fmt.Errorf("%w: invalid personalization operation %q", ErrInvalidArgument, seed.Operation)
	}
	if seed.Version == 1 && seed.Operation != PersonalizationOperationCreate && seed.Operation != PersonalizationOperationMigration {
		return fmt.Errorf("%w: initial personalization operation must be create or migration", ErrInvalidArgument)
	}
	if seed.Version > 1 && seed.Operation != PersonalizationOperationUpdate && seed.Operation != PersonalizationOperationReset && seed.Operation != PersonalizationOperationMigration {
		return fmt.Errorf("%w: later personalization operation must be owner update, reset or trusted migration", ErrInvalidArgument)
	}
	if seed.Version == 1 && (!seed.ParentID.Empty() || seed.ParentVersion != 0) {
		return fmt.Errorf("%w: initial personalization seed cannot have a parent", ErrInvalidArgument)
	}
	if seed.Version > 1 && (seed.ParentID.Empty() || seed.ParentVersion+1 != seed.Version) {
		return fmt.Errorf("%w: personalization parent must precede version", ErrInvalidArgument)
	}
	if seed.CreatedAt.IsZero() || seed.UpdatedAt.IsZero() || seed.UpdatedAt.Before(seed.CreatedAt) {
		return fmt.Errorf("%w: invalid personalization timestamps", ErrInvalidArgument)
	}
	for name, value := range seed.CommunicationStyle.values() {
		if !finite(value) || value < 0 || value > 1 {
			return fmt.Errorf("%w: communication style %q is out of range", ErrInvalidArgument, name)
		}
	}
	for name, value := range seed.Temperament.Traits() {
		if err := ValidatePersonaTrait(name, value, DefaultPersonaLimits); err != nil {
			return err
		}
	}
	for name, value := range seed.EmotionalDynamics.values() {
		if !finite(value) || value < 0 || value > 1 {
			return fmt.Errorf("%w: emotional dynamic %q is out of range", ErrInvalidArgument, name)
		}
	}
	switch seed.EmotionalDynamics.ConflictStyle {
	case "adaptive", "withdraw", "direct", "cold", "humor":
	default:
		return fmt.Errorf("%w: invalid conflict style", ErrInvalidArgument)
	}
	if err := validateIdentityPersonalization(seed.Identity); err != nil {
		return err
	}
	if err := seed.RelationshipSeed.Validate(); err != nil {
		return err
	}
	if err := validateEmotionalLists(seed.EmotionalDynamics); err != nil {
		return err
	}
	if err := validateStructuredBackstory(seed.Backstory); err != nil {
		return err
	}
	if err := validateEvolutionPolicy(seed.EvolutionPolicy, seed.Temperament); err != nil {
		return err
	}
	if utf8.RuneCountInString(strings.TrimSpace(seed.Reason)) > PersonalizationTextMaxRunes || strings.ContainsRune(seed.Reason, '\x00') {
		return fmt.Errorf("%w: personalization reason is invalid", ErrInvalidArgument)
	}
	if seed.Version > 1 && strings.TrimSpace(seed.Reason) == "" {
		return fmt.Errorf("%w: personalization revision reason is required", ErrInvalidArgument)
	}
	return nil
}

// MigrateLegacyBackstory losslessly structures a free-form owner backstory.
// Narrative remains the authoritative original; summary and episodes contain
// only deterministic excerpts from it, so migration cannot hallucinate facts.
// The caller is expected to persist the returned revision through a dedicated
// trusted migration boundary, never through reflection or model output.
func MigrateLegacyBackstory(seed PersonalizationSeed, now time.Time) (PersonalizationSeed, bool, error) {
	if err := seed.Validate(); err != nil {
		return PersonalizationSeed{}, false, err
	}
	narrative := strings.TrimSpace(seed.Backstory.Narrative)
	if narrative == "" || len(seed.Backstory.Episodes) > 0 {
		return seed, false, nil
	}
	if !now.After(seed.UpdatedAt) {
		now = seed.UpdatedAt.Add(time.Nanosecond)
	}
	digest := sha256.Sum256([]byte(narrative))
	digestPrefix := hex.EncodeToString(digest[:8])
	chunks := splitLegacyBackstory(narrative, BackstoryEpisodeMaxRunes)
	episodes := make([]BackstoryEpisode, 0, len(chunks))
	for index, chunk := range chunks {
		title := "Исходная предыстория"
		if len(chunks) > 1 {
			title = fmt.Sprintf("Исходная предыстория · часть %d", index+1)
		}
		episodes = append(episodes, BackstoryEpisode{
			ID: fmt.Sprintf("legacy-%s-%02d", digestPrefix, index+1), Title: title,
			Content: chunk, Kind: "legacy_narrative", Sequence: index + 1,
		})
	}

	migrated := seed
	migrated.Version = seed.Version + 1
	migrated.ParentID = seed.RevisionID
	migrated.ParentVersion = seed.Version
	migrated.RevisionID = ID(fmt.Sprintf("%s:personalization:v%d", seed.AgentID, migrated.Version))
	migrated.Operation = PersonalizationOperationMigration
	migrated.Backstory.Episodes = episodes
	if strings.TrimSpace(migrated.Backstory.Summary) == "" {
		migrated.Backstory.Summary = legacyBackstorySummary(narrative)
	}
	migrated.Reason = "lossless legacy backstory structuring"
	migrated.UpdatedAt = now.UTC()
	if err := migrated.Validate(); err != nil {
		return PersonalizationSeed{}, false, err
	}
	return migrated, true, nil
}

// BackstoryIdentitySummary returns the only backstory layer suitable for
// every prompt. It never returns the full narrative when that narrative is
// long; detailed episodes belong to selective memory recall.
func BackstoryIdentitySummary(backstory StructuredBackstory) string {
	if summary := strings.TrimSpace(backstory.Summary); summary != "" {
		return summary
	}
	if narrative := strings.TrimSpace(backstory.Narrative); narrative != "" {
		return legacyBackstorySummary(narrative)
	}
	titles := make([]string, 0, 8)
	for _, episode := range backstory.Episodes {
		if title := strings.TrimSpace(episode.Title); title != "" {
			titles = append(titles, title)
			if len(titles) == 8 {
				break
			}
		}
	}
	if len(titles) > 0 {
		return legacyBackstorySummary("Ключевые эпизоды прошлого: " + strings.Join(titles, "; "))
	}
	for _, episode := range backstory.Episodes {
		if content := strings.TrimSpace(episode.Content); content != "" {
			return legacyBackstorySummary(content)
		}
	}
	return ""
}

func splitLegacyBackstory(value string, limit int) []string {
	remaining := []rune(strings.TrimSpace(value))
	if limit <= 0 {
		limit = BackstoryEpisodeMaxRunes
	}
	result := make([]string, 0, (len(remaining)+limit-1)/limit)
	for len(remaining) > limit {
		cut := legacyBackstoryCut(remaining, limit)
		result = append(result, strings.TrimSpace(string(remaining[:cut])))
		remaining = []rune(strings.TrimLeftFunc(string(remaining[cut:]), func(value rune) bool {
			return value == ' ' || value == '\t' || value == '\r' || value == '\n'
		}))
	}
	if tail := strings.TrimSpace(string(remaining)); tail != "" {
		result = append(result, tail)
	}
	return result
}

func legacyBackstoryCut(value []rune, limit int) int {
	minimum := limit * 3 / 5
	for index := limit - 1; index >= minimum; index-- {
		switch value[index] {
		case '\n':
			return index + 1
		case '.', '!', '?', '…':
			if index+1 == len(value) || index+1 >= limit || value[index+1] == ' ' || value[index+1] == '\n' {
				return index + 1
			}
		}
	}
	return limit
}

func legacyBackstorySummary(value string) string {
	const summaryLimit = 600
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= summaryLimit {
		return string(runes)
	}
	cut := legacyBackstoryCut(runes, summaryLimit-1)
	return strings.TrimSpace(string(runes[:cut])) + "…"
}

func validateIdentityPersonalization(identity IdentityPersonalization) error {
	values := map[string]string{
		"preferred_language": identity.PreferredLanguage, "pronouns": identity.Pronouns,
		"user_address": identity.UserAddress, "self_description": identity.SelfDescription, "role": identity.Role,
	}
	for name, value := range values {
		limit := PersonalizationTextMaxRunes
		if name == "preferred_language" || name == "pronouns" {
			limit = 64
		} else if name == "user_address" {
			limit = 128
		}
		if utf8.RuneCountInString(strings.TrimSpace(value)) > limit || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: identity field %q is invalid", ErrInvalidArgument, name)
		}
	}
	return nil
}

func validateEmotionalLists(dynamics EmotionalDynamics) error {
	if len(dynamics.Triggers) > PersonalizationListMaxCount || len(dynamics.SoothingStrategies) > PersonalizationListMaxCount {
		return fmt.Errorf("%w: emotional dynamics list is too large", ErrInvalidArgument)
	}
	for emotion, triggers := range dynamics.Triggers {
		if !validDimensionName(emotion) || len(triggers) > PersonalizationListMaxCount {
			return fmt.Errorf("%w: invalid emotional trigger group", ErrInvalidArgument)
		}
		if err := validateShortStringList(triggers); err != nil {
			return err
		}
	}
	return validateShortStringList(dynamics.SoothingStrategies)
}

func validateShortStringList(values []string) error {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || utf8.RuneCountInString(value) > 256 || strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("%w: personalization list item is invalid", ErrInvalidArgument)
		}
	}
	return nil
}

func validateStructuredBackstory(backstory StructuredBackstory) error {
	if utf8.RuneCountInString(strings.TrimSpace(backstory.Narrative)) > AgentBackstoryMaxRunes || strings.ContainsRune(backstory.Narrative, '\x00') {
		return fmt.Errorf("%w: structured backstory narrative is invalid", ErrInvalidArgument)
	}
	if utf8.RuneCountInString(strings.TrimSpace(backstory.Summary)) > BackstorySummaryMaxRunes || strings.ContainsRune(backstory.Summary, '\x00') {
		return fmt.Errorf("%w: structured backstory summary is invalid", ErrInvalidArgument)
	}
	if len(backstory.Episodes) > BackstoryEpisodeMaxCount {
		return fmt.Errorf("%w: too many backstory episodes", ErrInvalidArgument)
	}
	seen := make(map[string]struct{}, len(backstory.Episodes))
	for _, episode := range backstory.Episodes {
		id := strings.TrimSpace(episode.ID)
		if id == "" || utf8.RuneCountInString(id) > 128 || strings.ContainsRune(id, '\x00') {
			return fmt.Errorf("%w: invalid backstory episode id", ErrInvalidArgument)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("%w: duplicate backstory episode id", ErrInvalidArgument)
		}
		seen[id] = struct{}{}
		if strings.TrimSpace(episode.Content) == "" || utf8.RuneCountInString(episode.Content) > BackstoryEpisodeMaxRunes || strings.ContainsRune(episode.Content, '\x00') {
			return fmt.Errorf("%w: invalid backstory episode content", ErrInvalidArgument)
		}
		if !finite(episode.EmotionalValence) || episode.EmotionalValence < -1 || episode.EmotionalValence > 1 || episode.Sequence < 0 {
			return fmt.Errorf("%w: invalid backstory episode metadata", ErrInvalidArgument)
		}
		if err := validateShortStringList(episode.People); err != nil {
			return err
		}
		for _, value := range []string{episode.Title, episode.Kind, episode.Place} {
			if utf8.RuneCountInString(strings.TrimSpace(value)) > 256 || strings.ContainsRune(value, '\x00') {
				return fmt.Errorf("%w: invalid backstory episode field", ErrInvalidArgument)
			}
		}
	}
	return nil
}

func validateEvolutionPolicy(policy PersonalizationEvolutionPolicy, temperament Temperament) error {
	if len(policy.LockedFields) > PersonalizationListMaxCount || len(policy.TraitBounds) > DefaultPersonaLimits.MaxTraits {
		return fmt.Errorf("%w: personalization evolution policy is too large", ErrInvalidArgument)
	}
	locks := append([]string(nil), policy.LockedFields...)
	sort.Strings(locks)
	for index, field := range locks {
		field = strings.TrimSpace(field)
		if field == "" || utf8.RuneCountInString(field) > 128 || strings.ContainsRune(field, '\x00') || (index > 0 && field == strings.TrimSpace(locks[index-1])) {
			return fmt.Errorf("%w: invalid or duplicate locked field", ErrInvalidArgument)
		}
	}
	traits := temperament.Traits()
	if policy.ReflectionMode != "" && policy.ReflectionMode != PersonalizationReflectionEnabled && policy.ReflectionMode != PersonalizationReflectionDisabled {
		return fmt.Errorf("%w: invalid personalization reflection mode", ErrInvalidArgument)
	}
	if policy.ReflectionCooldownMinutes < 0 || policy.ReflectionCooldownMinutes > 7*24*60 {
		return fmt.Errorf("%w: personalization reflection cooldown is out of range", ErrInvalidArgument)
	}
	if policy.ReflectionMaxTokens < 0 || policy.ReflectionMaxTokens > 10_000 || (policy.ReflectionMaxTokens > 0 && policy.ReflectionMaxTokens < 256) {
		return fmt.Errorf("%w: personalization reflection token budget is out of range", ErrInvalidArgument)
	}
	if policy.ReflectionMaxDurationSecs < 0 || policy.ReflectionMaxDurationSecs > 120 || (policy.ReflectionMaxDurationSecs > 0 && policy.ReflectionMaxDurationSecs < 5) {
		return fmt.Errorf("%w: personalization reflection duration budget is out of range", ErrInvalidArgument)
	}
	if policy.ReflectionMaxEvidence < 0 || policy.ReflectionMaxEvidence > 32 {
		return fmt.Errorf("%w: personalization reflection evidence budget is out of range", ErrInvalidArgument)
	}
	for name, valueRange := range policy.TraitBounds {
		if _, ok := traits[name]; !ok || !finite(valueRange.Min) || !finite(valueRange.Max) || valueRange.Min < 0 || valueRange.Max > 1 || valueRange.Min > valueRange.Max {
			return fmt.Errorf("%w: invalid evolution bound for %q", ErrInvalidArgument, name)
		}
	}
	return nil
}

// StandardTemperamentTraitNames returns a stable copy for migrations,
// compilers and UI contracts without exposing mutable package state.
func StandardTemperamentTraitNames() []string {
	return append([]string(nil), standardTemperamentTraits...)
}
