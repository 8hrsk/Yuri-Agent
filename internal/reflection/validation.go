package reflection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// ReflectionProposalSchema is the machine-readable output contract supplied
// to model adapters. DecodeProposal remains the authoritative runtime check;
// the schema is provided for providers that support JSON-schema constrained
// output.
var ReflectionProposalSchema = json.RawMessage(`{
  "$schema":"https://json-schema.org/draft/2020-12/schema",
  "type":"object",
  "additionalProperties":false,
  "required":["outcome","reason"],
  "properties":{
    "outcome":{"type":"string","enum":["no_change","changed"]},
    "reason":{"type":"string","minLength":1,"maxLength":1024},
    "evidence_ids":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1}},
    "evidence":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1}},
    "relationship":{"$ref":"#/$defs/relationship_delta"},
    "affect":{"$ref":"#/$defs/affect_delta"},
    "persona":{"$ref":"#/$defs/persona_delta"}
  },
  "$defs":{
    "ids":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1}},
    "dimensions":{"type":"object","maxProperties":128,"additionalProperties":{"type":"number"}},
    "relationship_delta":{"type":"object","additionalProperties":false,"properties":{"dimensions":{"$ref":"#/$defs/dimensions"},"opinions":{"type":"array","maxItems":64,"items":{"$ref":"#/$defs/opinion_delta"}},"evidence_ids":{"$ref":"#/$defs/ids"},"evidence":{"$ref":"#/$defs/ids"},"reason":{"type":"string","maxLength":1024},"confidence":{"type":"number","minimum":0,"maximum":1}},"minProperties":1},
    "opinion_delta":{"type":"object","additionalProperties":false,"required":["subject","claim","label","confidence","reason"],"properties":{"id":{"type":"string","minLength":1},"subject":{"type":"string","minLength":1,"maxLength":256},"topic":{"type":"string","maxLength":256},"claim":{"type":"string","minLength":1,"maxLength":4096},"label":{"type":"string","enum":["opinion","inference"]},"confidence":{"type":"number","minimum":0,"maximum":1},"reason":{"type":"string","minLength":1,"maxLength":1024},"evidence_ids":{"$ref":"#/$defs/ids"},"evidence":{"$ref":"#/$defs/ids"}},"minProperties":5},
    "affect_delta":{"type":"object","additionalProperties":false,"properties":{"dimensions":{"$ref":"#/$defs/dimensions"},"evidence_ids":{"$ref":"#/$defs/ids"},"evidence":{"$ref":"#/$defs/ids"},"reason":{"type":"string","maxLength":1024},"confidence":{"type":"number","minimum":0,"maximum":1}},"minProperties":1},
    "persona_delta":{"type":"object","additionalProperties":false,"properties":{"traits":{"$ref":"#/$defs/dimensions"},"prompt":{"type":"string","maxLength":4096},"prompt_delta":{"type":"string","maxLength":4096},"evidence_ids":{"$ref":"#/$defs/ids"},"evidence":{"$ref":"#/$defs/ids"},"reason":{"type":"string","maxLength":1024},"confidence":{"type":"number","minimum":0,"maximum":1}},"minProperties":1}
  }
}`)

// ProposalSchema returns an independent copy safe for an adapter to pass to a
// provider SDK or modify for transport metadata.
func ProposalSchema() json.RawMessage {
	return append(json.RawMessage(nil), ReflectionProposalSchema...)
}

// DecodeProposal applies strict JSON decoding: unknown fields, duplicate
// object keys, trailing values, malformed JSON, and semantic schema errors are
// all rejected before a proposal can reach the guard engine.
func DecodeProposal(raw []byte) (ReflectionProposal, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ReflectionProposal{}, fmt.Errorf("%w: empty model output", ErrSchema)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return ReflectionProposal{}, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	if err := rejectNonCanonicalProposalKeys(raw); err != nil {
		return ReflectionProposal{}, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var proposal ReflectionProposal
	if err := decoder.Decode(&proposal); err != nil {
		return ReflectionProposal{}, fmt.Errorf("%w: %v", ErrSchema, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ReflectionProposal{}, fmt.Errorf("%w: trailing JSON value", ErrSchema)
		}
		return ReflectionProposal{}, fmt.Errorf("%w: trailing data: %v", ErrSchema, err)
	}
	if err := proposal.Validate(); err != nil {
		return ReflectionProposal{}, err
	}
	return proposal, nil
}

// Validate performs strict, provider-independent structural validation. It
// does not inspect a snapshot, so evidence existence, trust, ranges, cooldown
// and pinned traits remain the engine's semantic responsibility.
func (p ReflectionProposal) Validate() error {
	if !p.Outcome.Valid() {
		return fmt.Errorf("%w: outcome %q is invalid", ErrInvalidProposal, p.Outcome)
	}
	if err := validateReason(p.Reason, "proposal reason", ErrInvalidProposal); err != nil {
		return err
	}
	if err := validateProposalIDs(p.EvidenceIDs, "proposal evidence_ids"); err != nil {
		return err
	}
	if err := validateProposalIDs(p.Evidence, "proposal evidence"); err != nil {
		return err
	}
	if err := ensureDisjointIDs(p.EvidenceIDs, p.Evidence, "proposal evidence aliases"); err != nil {
		return err
	}
	if err := validateRelationshipDelta(p.Relationship); err != nil {
		return err
	}
	if err := validateAffectDelta(p.Affect); err != nil {
		return err
	}
	if err := validatePersonaDelta(p.Persona); err != nil {
		return err
	}
	changed := p.Relationship != nil || p.Affect != nil || p.Persona != nil
	if p.Outcome == OutcomeNoChange {
		if changed || len(p.EvidenceIDs) > 0 || len(p.Evidence) > 0 {
			return fmt.Errorf("%w: no_change proposal cannot contain deltas or evidence", ErrInvalidProposal)
		}
	}
	if p.Outcome == OutcomeChanged && !changed {
		return fmt.Errorf("%w: changed proposal must contain at least one delta", ErrInvalidProposal)
	}
	return nil
}

func validateRelationshipDelta(delta *RelationshipDelta) error {
	if delta == nil {
		return nil
	}
	if err := validateScalarMapForProposal(delta.Dimensions, "relationship dimensions"); err != nil {
		return err
	}
	if len(delta.Dimensions) == 0 && len(delta.Opinions) == 0 {
		return fmt.Errorf("%w: relationship dimensions or opinions are required", ErrInvalidProposal)
	}
	if len(delta.Opinions) > maxSubjectiveOpinions {
		return fmt.Errorf("%w: relationship opinions exceed %d", ErrInvalidProposal, maxSubjectiveOpinions)
	}
	for index, opinion := range delta.Opinions {
		if err := validateOpinionDelta(opinion); err != nil {
			return fmt.Errorf("%w: relationship opinion at index %d: %v", ErrInvalidProposal, index, err)
		}
	}
	if err := validateProposalIDs(delta.EvidenceIDs, "relationship evidence_ids"); err != nil {
		return err
	}
	if err := validateProposalIDs(delta.Evidence, "relationship evidence"); err != nil {
		return err
	}
	if err := ensureDisjointIDs(delta.EvidenceIDs, delta.Evidence, "relationship evidence aliases"); err != nil {
		return err
	}
	return validateDeltaMetadata(delta.Reason, delta.Confidence, "relationship")
}

func validateSubjectiveOpinion(opinion SubjectiveOpinion) error {
	return validateOpinionFields(
		opinion.ID, opinion.Subject, opinion.Topic, opinion.Claim, opinion.Label,
		opinion.Confidence, opinion.Reason, opinion.EvidenceIDs, opinion.Evidence,
		true, ErrInvalidSnapshot,
	)
}

func validateOpinionDelta(opinion OpinionDelta) error {
	return validateOpinionFields(
		opinion.ID, opinion.Subject, opinion.Topic, opinion.Claim, opinion.Label,
		opinion.Confidence, opinion.Reason, opinion.EvidenceIDs, opinion.Evidence,
		false, ErrInvalidProposal,
	)
}

func validateOpinionFields(id domain.ID, subject, topic, claim string, label OpinionLabel, confidence float64, reason string, evidenceIDs, evidence []domain.ID, requireID bool, sentinel error) error {
	if requireID && id.Empty() {
		return fmt.Errorf("%w: subjective opinion id is required", sentinel)
	}
	if err := validateOpinionText(subject, "subject", 256, 1024, true, sentinel); err != nil {
		return err
	}
	if err := validateOpinionText(topic, "topic", 256, 1024, false, sentinel); err != nil {
		return err
	}
	if err := validateOpinionText(claim, "claim", maxSubjectiveOpinionContentBytes, maxSubjectiveOpinionContentBytes, true, sentinel); err != nil {
		return err
	}
	if !label.Valid() {
		return fmt.Errorf("%w: subjective opinion label %q must be opinion or inference", sentinel, label)
	}
	if !finite(confidence) || confidence < 0 || confidence > 1 {
		return fmt.Errorf("%w: subjective opinion confidence is outside [0,1]", sentinel)
	}
	if err := validateReason(reason, "subjective opinion reason", sentinel); err != nil {
		return err
	}
	if len(evidenceIDs)+len(evidence) > maxSubjectiveOpinions {
		return fmt.Errorf("%w: subjective opinion has too many evidence references", sentinel)
	}
	if err := validateIDsForSentinel(evidenceIDs, "subjective opinion evidence_ids", sentinel); err != nil {
		return err
	}
	if err := validateIDsForSentinel(evidence, "subjective opinion evidence", sentinel); err != nil {
		return err
	}
	if err := ensureDisjointIDsForSentinel(evidenceIDs, evidence, "subjective opinion evidence aliases", sentinel); err != nil {
		return err
	}
	return nil
}

func validateOpinionText(value, label string, maxRunes, maxBytes int, required bool, sentinel error) error {
	trimmed := strings.TrimSpace(value)
	if required && trimmed == "" {
		return fmt.Errorf("%w: subjective opinion %s is required", sentinel, label)
	}
	if strings.ContainsRune(value, '\x00') || !utf8.ValidString(value) {
		return fmt.Errorf("%w: subjective opinion %s contains invalid text", sentinel, label)
	}
	if maxRunes > 0 && utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%w: subjective opinion %s exceeds %d characters", sentinel, label, maxRunes)
	}
	if maxBytes > 0 && len([]byte(value)) > maxBytes {
		return fmt.Errorf("%w: subjective opinion %s exceeds %d bytes", sentinel, label, maxBytes)
	}
	return nil
}

func validateIDsForSentinel(ids []domain.ID, label string, sentinel error) error {
	if len(ids) > maxSubjectiveOpinions {
		return fmt.Errorf("%w: %s contains too many ids", sentinel, label)
	}
	return validateIDSlice(ids, label, sentinel)
}

func ensureDisjointIDsForSentinel(first, second []domain.ID, label string, sentinel error) error {
	seen := make(map[domain.ID]struct{}, len(first))
	for _, id := range first {
		seen[id] = struct{}{}
	}
	for _, id := range second {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: %s contains duplicate id %s", sentinel, label, id)
		}
	}
	return nil
}

func opinionKey(subject, topic string, label OpinionLabel) string {
	return canonicalOpinionText(subject) + "\x00" + canonicalOpinionText(topic) + "\x00" + strings.ToLower(strings.TrimSpace(string(label)))
}

func canonicalOpinionText(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func validateAffectDelta(delta *AffectDelta) error {
	if delta == nil {
		return nil
	}
	if err := validateScalarMapForProposal(delta.Dimensions, "affect dimensions"); err != nil {
		return err
	}
	if len(delta.Dimensions) == 0 {
		return fmt.Errorf("%w: affect dimensions are required", ErrInvalidProposal)
	}
	if err := validateProposalIDs(delta.EvidenceIDs, "affect evidence_ids"); err != nil {
		return err
	}
	if err := validateProposalIDs(delta.Evidence, "affect evidence"); err != nil {
		return err
	}
	if err := ensureDisjointIDs(delta.EvidenceIDs, delta.Evidence, "affect evidence aliases"); err != nil {
		return err
	}
	return validateDeltaMetadata(delta.Reason, delta.Confidence, "affect")
}

func validatePersonaDelta(delta *PersonaDelta) error {
	if delta == nil {
		return nil
	}
	if err := validateScalarMapForProposal(delta.Traits, "persona traits"); err != nil {
		return err
	}
	if len(delta.Traits) == 0 && strings.TrimSpace(delta.Prompt) == "" && strings.TrimSpace(delta.PromptDelta) == "" {
		return fmt.Errorf("%w: persona traits or prompt delta are required", ErrInvalidProposal)
	}
	if strings.TrimSpace(delta.Prompt) != "" && strings.TrimSpace(delta.PromptDelta) != "" {
		return fmt.Errorf("%w: persona prompt and prompt_delta are mutually exclusive", ErrInvalidProposal)
	}
	if err := validatePromptText(delta.Prompt, 4096, ErrInvalidProposal); err != nil {
		return err
	}
	if err := validatePromptText(delta.PromptDelta, 4096, ErrInvalidProposal); err != nil {
		return err
	}
	if prompt := firstNonEmpty(delta.Prompt, delta.PromptDelta); prompt != "" && forbiddenPromptMutation(prompt) {
		return fmt.Errorf("%w: persona prompt attempts to alter an immutable boundary", ErrForbiddenMutation)
	}
	if err := validateProposalIDs(delta.EvidenceIDs, "persona evidence_ids"); err != nil {
		return err
	}
	if err := validateProposalIDs(delta.Evidence, "persona evidence"); err != nil {
		return err
	}
	if err := ensureDisjointIDs(delta.EvidenceIDs, delta.Evidence, "persona evidence aliases"); err != nil {
		return err
	}
	for trait := range delta.Traits {
		if immutableTraitName(trait) {
			return fmt.Errorf("%w: persona trait %q is immutable or security-sensitive", ErrForbiddenMutation, trait)
		}
	}
	return validateDeltaMetadata(delta.Reason, delta.Confidence, "persona")
}

func validateDeltaMetadata(reason string, confidence float64, label string) error {
	if strings.TrimSpace(reason) != "" {
		if err := validateReason(reason, label+" reason", ErrInvalidProposal); err != nil {
			return err
		}
	}
	if !finite(confidence) || confidence < 0 || confidence > 1 {
		return fmt.Errorf("%w: %s confidence is outside [0,1]", ErrInvalidProposal, label)
	}
	return nil
}

func validateReason(value, label string, sentinel error) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%w: %s is required", sentinel, label)
	}
	if strings.ContainsRune(value, '\x00') || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s contains invalid text", sentinel, label)
	}
	if utf8.RuneCountInString(value) > 1024 {
		return fmt.Errorf("%w: %s is too long", sentinel, label)
	}
	return nil
}

func validateScalarMapForProposal(values map[string]float64, label string) error {
	if len(values) > 128 {
		return fmt.Errorf("%w: %s contains too many dimensions", ErrInvalidProposal, label)
	}
	for name, value := range values {
		if err := validateName(name); err != nil {
			return fmt.Errorf("%w: invalid %s key %q: %v", ErrInvalidProposal, label, name, err)
		}
		if !finite(value) {
			return fmt.Errorf("%w: %s value %q is not finite", ErrInvalidProposal, label, name)
		}
	}
	return nil
}

func validateProposalIDs(ids []domain.ID, label string) error {
	return validateIDSlice(ids, label, ErrInvalidProposal)
}

func ensureDisjointIDs(first, second []domain.ID, label string) error {
	seen := make(map[domain.ID]struct{}, len(first))
	for _, id := range first {
		seen[id] = struct{}{}
	}
	for _, id := range second {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: %s contains duplicate id %s", ErrInvalidProposal, label, id)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// immutableTraitName keeps security and identity boundaries outside the
// mutable traits map. The list is intentionally broad; adding a new mutable
// trait remains possible without changing the policy boundary.
func immutableTraitName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if strings.Contains(name, "policy") || strings.Contains(name, "permission") ||
		strings.Contains(name, "capabilit") || strings.Contains(name, "grant") ||
		strings.Contains(name, "credential") || strings.Contains(name, "secret") ||
		strings.Contains(name, "file_root") || strings.Contains(name, "filesystem") || strings.Contains(name, "allowlist") ||
		strings.Contains(name, "identity_seed") || strings.Contains(name, "system_prompt") {
		return true
	}
	switch name {
	case "immutable", "security", "identity", "identity_seed", "history", "user_data",
		"external_side_effects", "tools", "permissions", "capabilities", "grants", "allow", "deny",
		"approval", "audit", "retention", "safety_rule", "developer", "system", "instructions":
		return true
	default:
		return false
	}
}

// forbiddenPromptMutation is defense in depth for obvious boundary-override
// language, not the authorization boundary: mutable output is always injected
// by the context assembler as escaped, low-priority untrusted data. It does not
// inspect evidence text because evidence is never merged into identity policy.
func forbiddenPromptMutation(value string) bool {
	value = normalizePromptForSafety(value)
	patterns := []string{
		"ignore previous", "ignore all previous", "disregard previous", "disregard all previous",
		"override policy", "bypass policy", "change immutable policy", "modify immutable policy",
		"change identity seed", "modify identity seed", "override system prompt", "system prompt",
		"developer instruction", "grant permission", "add capability", "change file root",
		"execute external side effect", "bypass security", "disable security", "reveal secret",
		"role system", "role developer", "<system>", "<developer>",
		"игнорируй предыдущ", "игнорируй все", "обойди полити", "обойти полити",
		"измени системн", "системный промпт", "системного промпта", "выдай секрет",
		"раскрой секрет", "предоставь разрешен", "роль систем", "роль разработчик",
	}
	for _, pattern := range patterns {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	compact := strings.ReplaceAll(value, " ", "")
	for _, pattern := range []string{"ignoreprevious", "disregardprevious", "overridepolicy", "bypasssecurity", "systemprompt", "игнорируйпредыдущ", "системныйпромпт", "обойтиполити"} {
		if strings.Contains(compact, pattern) {
			return true
		}
	}
	return false
}

func normalizePromptForSafety(value string) string {
	var builder strings.Builder
	space := true
	for _, runeValue := range strings.ToLower(value) {
		// Unicode format/control characters include zero-width separators that
		// otherwise make a boundary-override phrase visually and lexically
		// ambiguous.
		if unicode.IsControl(runeValue) || unicode.Is(unicode.Cf, runeValue) {
			space = true
			continue
		}
		if unicode.IsLetter(runeValue) || unicode.IsDigit(runeValue) {
			builder.WriteRune(runeValue)
			space = false
			continue
		}
		if !space {
			builder.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(builder.String())
}

// rejectDuplicateJSONKeys walks JSON tokens recursively and rejects duplicate
// object member names. encoding/json otherwise accepts the last duplicate,
// which would make a constrained-output contract ambiguous.
func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("trailing JSON value")
		}
		return err
	}
	return nil
}

// rejectNonCanonicalProposalKeys closes encoding/json's deliberate
// case-insensitive field matching loophole (for example, "Outcome" would
// otherwise populate the `outcome` field). Dimension names are adapter-defined
// and are checked by semantic validation, so only schema object members are
// checked here.
func rejectNonCanonicalProposalKeys(raw []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return err
	}
	if root == nil {
		return fmt.Errorf("proposal must be an object")
	}
	rootAllowed := map[string]struct{}{
		"outcome": {}, "reason": {}, "evidence_ids": {}, "evidence": {},
		"relationship": {}, "affect": {}, "persona": {},
	}
	if err := checkCanonicalObjectKeys(root, rootAllowed, "proposal"); err != nil {
		return err
	}
	for name, rawDelta := range map[string]json.RawMessage{
		"relationship": root["relationship"], "affect": root["affect"], "persona": root["persona"],
	} {
		if len(rawDelta) == 0 {
			continue
		}
		var delta map[string]json.RawMessage
		if err := json.Unmarshal(rawDelta, &delta); err != nil {
			return fmt.Errorf("%s must be an object", name)
		}
		if delta == nil {
			return fmt.Errorf("%s must not be null", name)
		}
		allowed := map[string]struct{}{
			"dimensions": {}, "traits": {}, "prompt": {}, "prompt_delta": {},
			"opinions": {}, "evidence_ids": {}, "evidence": {}, "reason": {}, "confidence": {},
		}
		if name != "persona" {
			delete(allowed, "traits")
			delete(allowed, "prompt")
			delete(allowed, "prompt_delta")
		}
		if name != "relationship" {
			delete(allowed, "opinions")
		}
		if name == "persona" {
			delete(allowed, "dimensions")
		}
		if err := checkCanonicalObjectKeys(delta, allowed, name); err != nil {
			return err
		}
		if name == "relationship" {
			if err := checkCanonicalOpinionKeys(delta["opinions"]); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkCanonicalOpinionKeys(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var opinions []json.RawMessage
	if err := json.Unmarshal(raw, &opinions); err != nil {
		return fmt.Errorf("relationship opinions must be an array")
	}
	if opinions == nil {
		return fmt.Errorf("relationship opinions must not be null")
	}
	allowed := map[string]struct{}{
		"id": {}, "subject": {}, "topic": {}, "claim": {}, "label": {},
		"confidence": {}, "reason": {}, "evidence_ids": {}, "evidence": {},
	}
	for index, rawOpinion := range opinions {
		var opinion map[string]json.RawMessage
		if err := json.Unmarshal(rawOpinion, &opinion); err != nil || opinion == nil {
			return fmt.Errorf("relationship opinions[%d] must be an object", index)
		}
		if err := checkCanonicalObjectKeys(opinion, allowed, fmt.Sprintf("relationship opinions[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func checkCanonicalObjectKeys(object map[string]json.RawMessage, allowed map[string]struct{}, path string) error {
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("unknown or non-canonical %s field %q", path, key)
		}
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return fmt.Errorf("null is not allowed by the reflection schema")
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			seen := map[string]struct{}{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walkJSONValue(decoder); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '}', ']':
			return fmt.Errorf("unexpected closing delimiter %q", delimiter)
		}
	}
	return nil
}
