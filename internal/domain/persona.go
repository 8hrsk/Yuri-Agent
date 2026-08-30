package domain

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// PersonaOperation describes why a mutable persona revision was written. The
// operation is metadata only: it never changes the immutable policy or the
// identity seed.
type PersonaOperation string

const (
	PersonaOperationCreate   PersonaOperation = "create"
	PersonaOperationUpdate   PersonaOperation = "update"
	PersonaOperationRollback PersonaOperation = "rollback"
	PersonaOperationReset    PersonaOperation = "reset"
	PersonaOperationPin      PersonaOperation = "pin"
)

func (o PersonaOperation) Valid() bool {
	switch o {
	case PersonaOperationCreate, PersonaOperationUpdate, PersonaOperationRollback,
		PersonaOperationReset, PersonaOperationPin:
		return true
	default:
		return false
	}
}

// PersonaTraitName is deliberately a string rather than an enum. Product
// traits may grow over time (for example speech habits), while validation
// still rejects names that could be confused with immutable policy fields.
type PersonaTraitName string

const (
	TraitWarmth             PersonaTraitName = "warmth"
	TraitTrust              PersonaTraitName = "trust"
	TraitAttachment         PersonaTraitName = "attachment"
	TraitJealousy           PersonaTraitName = "jealousy"
	TraitIrritability       PersonaTraitName = "irritability"
	TraitRomanticTone       PersonaTraitName = "romantic_tone"
	TraitEmotionality       PersonaTraitName = "emotionality"
	TraitDirectness         PersonaTraitName = "directness"
	TraitPlayfulness        PersonaTraitName = "playfulness"
	TraitFormality          PersonaTraitName = "formality"
	TraitInitiative         PersonaTraitName = "initiative"
	TraitEmpathy            PersonaTraitName = "empathy"
	TraitSociability        PersonaTraitName = "sociability"
	TraitShyness            PersonaTraitName = "shyness"
	TraitAnxiety            PersonaTraitName = "anxiety"
	TraitFearfulness        PersonaTraitName = "fearfulness"
	TraitEmotionalStability PersonaTraitName = "emotional_stability"
	TraitSensitivity        PersonaTraitName = "sensitivity"
	TraitPossessiveness     PersonaTraitName = "possessiveness"
	TraitImpulsivity        PersonaTraitName = "impulsivity"
	TraitStubbornness       PersonaTraitName = "stubbornness"
	TraitOptimism           PersonaTraitName = "optimism"
	TraitCuriosity          PersonaTraitName = "curiosity"
	TraitSuspicion          PersonaTraitName = "suspicion"
	TraitTsundere           PersonaTraitName = "tsundere"
)

// CommonPersonaTraits is the stable set used by the default seed. Custom
// traits are allowed when they use a safe snake_case name so this layer does
// not need to change whenever a new speaking habit is introduced.
var CommonPersonaTraits = map[PersonaTraitName]struct{}{
	TraitWarmth: {}, TraitTrust: {}, TraitAttachment: {}, TraitJealousy: {},
	TraitIrritability: {}, TraitRomanticTone: {}, TraitEmotionality: {},
	TraitDirectness: {}, TraitPlayfulness: {}, TraitFormality: {}, TraitInitiative: {},
	TraitEmpathy: {}, TraitSociability: {}, TraitShyness: {}, TraitAnxiety: {},
	TraitFearfulness: {}, TraitEmotionalStability: {}, TraitSensitivity: {},
	TraitPossessiveness: {}, TraitImpulsivity: {}, TraitStubbornness: {},
	TraitOptimism: {}, TraitCuriosity: {}, TraitSuspicion: {}, TraitTsundere: {},
}

// PersonaLimits bounds evolution. Values are normalized to [0, 1] and a
// regular reflection may move one trait by at most MaxDelta. Rollback/reset
// operations intentionally bypass MaxDelta because they restore an existing
// trusted snapshot rather than accepting a new model-generated value.
type PersonaLimits struct {
	MinValue        float64
	MaxValue        float64
	MaxDelta        float64
	MaxTraits       int
	MaxPromptBytes  int
	RequireEvidence bool
}

// DefaultPersonaLimits are conservative defaults for autonomous reflection.
// Applications may use stricter limits; loosening them should require an
// explicit product decision rather than happen through untrusted input.
var DefaultPersonaLimits = PersonaLimits{
	MinValue:        0,
	MaxValue:        1,
	MaxDelta:        0.15,
	MaxTraits:       64,
	MaxPromptBytes:  16 * 1024,
	RequireEvidence: true,
}

func (l PersonaLimits) valid() bool {
	return finite(l.MinValue) && finite(l.MaxValue) && finite(l.MaxDelta) &&
		l.MinValue <= l.MaxValue && l.MinValue >= -1 && l.MaxValue <= 1 &&
		l.MaxDelta >= 0 && l.MaxDelta <= 1 && l.MaxTraits >= 1 &&
		l.MaxTraits <= 1024 && l.MaxPromptBytes >= 0 && l.MaxPromptBytes <= 1<<20
}

// MutablePersona is one immutable journal snapshot of the mutable persona.
// The identity seed and immutable policy are intentionally absent: no persona
// write can mutate either boundary by construction.
type MutablePersona struct {
	ID             ID                 `json:"id"`
	RevisionID     ID                 `json:"revision_id,omitempty"`
	Version        uint64             `json:"version"`
	ParentID       ID                 `json:"parent_id,omitempty"`
	ParentVersion  uint64             `json:"parent_version,omitempty"`
	Operation      PersonaOperation   `json:"operation,omitempty"`
	Traits         map[string]float64 `json:"traits"`
	Diff           map[string]float64 `json:"diff,omitempty"`
	PinnedTraits   []string           `json:"pinned_traits,omitempty"`
	IdentityPrompt string             `json:"identity_prompt,omitempty"`
	// PromptText follows the storage/data-model naming. IdentityPrompt is the
	// domain-facing name; repositories normalize both to the same value.
	PromptText  string         `json:"prompt_text,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Evidence    []EvidenceLink `json:"evidence,omitempty"`
	AuthorRunID ID             `json:"author_run_id,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// PersonaVersion is the architectural name for an immutable persona
// snapshot. It is an alias so callers can use either vocabulary without
// converting values or weakening the versioned boundary.
type PersonaVersion = MutablePersona

// PersonaVersionRecord is an immutable history envelope shared by storage
// adapters and reflection/application services.
type PersonaVersionRecord struct {
	Persona       MutablePersona     `json:"persona"`
	RevisionID    ID                 `json:"revision_id"`
	ParentID      ID                 `json:"parent_id,omitempty"`
	ParentVersion uint64             `json:"parent_version,omitempty"`
	Operation     PersonaOperation   `json:"operation"`
	Diff          map[string]float64 `json:"diff,omitempty"`
	Reason        string             `json:"reason,omitempty"`
	Evidence      []EvidenceLink     `json:"evidence,omitempty"`
	AuthorRunID   ID                 `json:"author_run_id,omitempty"`
}

func NewMutablePersona(id ID, traits map[string]float64, prompt string, now time.Time) (MutablePersona, error) {
	if id.Empty() || now.IsZero() {
		return MutablePersona{}, fmt.Errorf("%w: persona id and timestamp are required", ErrInvalidArgument)
	}
	result := MutablePersona{
		ID: id, Version: 1, Operation: PersonaOperationCreate,
		Traits: cloneFloatMap(traits), IdentityPrompt: strings.TrimSpace(prompt),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := result.Validate(); err != nil {
		return MutablePersona{}, err
	}
	return result, nil
}

// Prompt returns the normalized prompt text regardless of which compatibility
// field a caller populated.
func (p MutablePersona) Prompt() string {
	if strings.TrimSpace(p.IdentityPrompt) != "" {
		return p.IdentityPrompt
	}
	return p.PromptText
}

func (p MutablePersona) Valid() bool { return p.Validate() == nil }

func (p MutablePersona) Validate() error {
	limits := DefaultPersonaLimits
	if err := p.ValidateWithLimits(limits); err != nil {
		return err
	}
	if p.Operation != "" && !p.Operation.Valid() {
		return fmt.Errorf("%w: invalid persona operation %q", ErrInvalidArgument, p.Operation)
	}
	if strings.TrimSpace(p.IdentityPrompt) != "" && strings.TrimSpace(p.PromptText) != "" && strings.TrimSpace(p.IdentityPrompt) != strings.TrimSpace(p.PromptText) {
		return fmt.Errorf("%w: identity_prompt and prompt_text differ", ErrInvalidArgument)
	}
	for _, evidence := range p.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (p MutablePersona) ValidateWithLimits(limits PersonaLimits) error {
	if !limits.valid() {
		return fmt.Errorf("%w: invalid persona limits", ErrInvalidArgument)
	}
	if p.ID.Empty() || p.Version == 0 {
		return fmt.Errorf("%w: persona id and positive version are required", ErrInvalidArgument)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: persona timestamps are required", ErrInvalidArgument)
	}
	if len(p.Traits) > limits.MaxTraits {
		return fmt.Errorf("%w: too many persona traits", ErrInvalidArgument)
	}
	for name, value := range p.Traits {
		if err := ValidatePersonaTrait(name, value, limits); err != nil {
			return err
		}
	}
	if err := validatePinnedTraits(p.Traits, p.PinnedTraits); err != nil {
		return err
	}
	if len(p.Diff) > limits.MaxTraits {
		return fmt.Errorf("%w: too many persona diff traits", ErrInvalidArgument)
	}
	for name, value := range p.Diff {
		if err := ValidatePersonaTrait(name, value, PersonaLimits{MinValue: -1, MaxValue: 1, MaxDelta: 1, MaxTraits: limits.MaxTraits, MaxPromptBytes: limits.MaxPromptBytes}); err != nil {
			return err
		}
	}
	if limits.MaxPromptBytes > 0 && len([]byte(p.Prompt())) > limits.MaxPromptBytes {
		return fmt.Errorf("%w: persona prompt is too large", ErrInvalidArgument)
	}
	return nil
}

func ValidatePersonaTrait(name string, value float64, limits PersonaLimits) error {
	name = strings.TrimSpace(name)
	if !validTraitName(name) {
		return fmt.Errorf("%w: invalid persona trait name %q", ErrInvalidArgument, name)
	}
	if !finite(value) || value < limits.MinValue || value > limits.MaxValue {
		return fmt.Errorf("%w: persona trait %q is out of range", ErrInvalidArgument, name)
	}
	return nil
}

func ValidatePersonaTraits(traits map[string]float64) error {
	limits := DefaultPersonaLimits
	if len(traits) > limits.MaxTraits {
		return fmt.Errorf("%w: too many persona traits", ErrInvalidArgument)
	}
	for name, value := range traits {
		if err := ValidatePersonaTrait(name, value, limits); err != nil {
			return err
		}
	}
	return nil
}

func validatePinnedTraits(traits map[string]float64, pinned []string) error {
	seen := make(map[string]struct{}, len(pinned))
	for _, raw := range pinned {
		name := strings.TrimSpace(raw)
		if !validTraitName(name) {
			return fmt.Errorf("%w: invalid pinned persona trait %q", ErrInvalidArgument, raw)
		}
		if _, ok := traits[name]; !ok {
			return fmt.Errorf("%w: pinned trait %q is not present", ErrInvalidArgument, name)
		}
		if _, duplicate := seen[name]; duplicate {
			return fmt.Errorf("%w: duplicate pinned trait %q", ErrInvalidArgument, name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validTraitName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for index, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '_' && index > 0) {
			continue
		}
		return false
	}
	if name[0] == '_' || name[len(name)-1] == '_' {
		return false
	}
	return !immutablePersonaName(name)
}

func immutablePersonaName(name string) bool {
	for _, denied := range []string{
		"policy", "security", "capability", "capabilities", "grant", "permission",
		"approval", "audit", "retention", "file_root", "filesystem", "credential",
		"secret", "identity_seed", "allow", "deny", "safety_rule",
	} {
		if name == denied || strings.Contains(name, denied) {
			return true
		}
	}
	return false
}

// ValidatePersonaEvolution enforces max-delta and pin invariants for a
// regular autonomous update. Rollback/reset are handled by repositories with
// explicit operation metadata and are intentionally not passed here.
func ValidatePersonaEvolution(previous, next MutablePersona, limits PersonaLimits) error {
	if err := previous.ValidateWithLimits(limits); err != nil {
		return err
	}
	if err := next.ValidateWithLimits(limits); err != nil {
		return err
	}
	if next.Version != previous.Version+1 {
		return fmt.Errorf("%w: persona version must advance by one", ErrConflict)
	}
	if next.ParentVersion != 0 && next.ParentVersion != previous.Version {
		return fmt.Errorf("%w: persona parent version does not match current version", ErrConflict)
	}
	previousPinned := make(map[string]struct{}, len(previous.PinnedTraits))
	for _, name := range previous.PinnedTraits {
		previousPinned[name] = struct{}{}
		value, ok := next.Traits[name]
		if !ok || value != previous.Traits[name] {
			return fmt.Errorf("%w: pinned persona trait %q cannot be changed", ErrNotPermitted, name)
		}
	}
	for name, oldValue := range previous.Traits {
		newValue, ok := next.Traits[name]
		if !ok {
			if _, pinned := previousPinned[name]; pinned {
				return fmt.Errorf("%w: pinned persona trait %q cannot be removed", ErrNotPermitted, name)
			}
			continue
		}
		if math.Abs(newValue-oldValue) > limits.MaxDelta+1e-9 {
			return fmt.Errorf("%w: persona trait %q changed beyond max delta", ErrInvalidArgument, name)
		}
	}
	if limits.RequireEvidence && len(next.Evidence) == 0 {
		return fmt.Errorf("%w: persona evolution requires evidence", ErrInvalidArgument)
	}
	if strings.TrimSpace(next.Reason) == "" {
		return fmt.Errorf("%w: persona evolution reason is required", ErrInvalidArgument)
	}
	return nil
}

func (p MutablePersona) DeltaFrom(previous MutablePersona) map[string]float64 {
	result := make(map[string]float64)
	for name, value := range p.Traits {
		result[name] = value - previous.Traits[name]
	}
	for name, value := range previous.Traits {
		if _, ok := p.Traits[name]; !ok {
			result[name] = -value
		}
	}
	return result
}

// SortedPersonaTraitNames returns a deterministic copy for JSON/storage
// normalization and UI history rendering.
func SortedPersonaTraitNames(names []string) []string {
	result := append([]string(nil), names...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
	}
	sort.Strings(result)
	return result
}
