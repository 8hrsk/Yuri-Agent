package reflection

// Trigger identifies why a reflection run was requested.
type Trigger string

const (
	TriggerPostTurn     Trigger = "post_turn"
	TriggerIdle         Trigger = "idle"
	TriggerCron         Trigger = "cron"
	TriggerBeforeComp   Trigger = "before_compression"
	TriggerSessionEnd   Trigger = "session_end"
	TriggerManual       Trigger = "manual"
	TriggerPeerDialogue Trigger = "peer_dialogue"
)

// Valid reports whether the trigger is part of the stable reflection
// vocabulary. Unknown triggers are rejected so they cannot silently alter
// scheduling or audit semantics.
func (t Trigger) Valid() bool {
	switch t {
	case TriggerPostTurn, TriggerIdle, TriggerCron, TriggerBeforeComp,
		TriggerSessionEnd, TriggerManual, TriggerPeerDialogue:
		return true
	default:
		return false
	}
}

// Outcome is the only two outcomes an analyzer may request. Guards return a
// no-change result with an explanatory decision while malformed or unsafe
// proposals return an error and never produce an applied state.
type Outcome string

const (
	OutcomeNoChange Outcome = "no_change"
	OutcomeChanged  Outcome = "changed"
)

func (o Outcome) Valid() bool { return o == OutcomeNoChange || o == OutcomeChanged }

// Decision is a stable machine-readable explanation for a reflection result.
type Decision string

const (
	DecisionApplied     Decision = "applied"
	DecisionNoChange    Decision = "no_change"
	DecisionCooldown    Decision = "cooldown"
	DecisionNoEvidence  Decision = "insufficient_evidence"
	DecisionPinnedTrait Decision = "pinned_trait"
	DecisionDeltaLimit  Decision = "max_delta"
	DecisionUntrusted   Decision = "untrusted_evidence"
	DecisionBudget      Decision = "budget"
	DecisionCancelled   Decision = "cancelled"
)

// EvidenceSource is provenance for a snapshot item. External sources are
// intentionally distinct from user/agent transcript evidence.
type EvidenceSource string

const (
	EvidenceSourceUser       EvidenceSource = "user"
	EvidenceSourceAssistant  EvidenceSource = "assistant"
	EvidenceSourceSystem     EvidenceSource = "system"
	EvidenceSourceTranscript EvidenceSource = "transcript"
	EvidenceSourceMemory     EvidenceSource = "memory"
	EvidenceSourceTool       EvidenceSource = "tool"
	EvidenceSourceFile       EvidenceSource = "file"
	EvidenceSourceWeb        EvidenceSource = "web"
	EvidenceSourcePlugin     EvidenceSource = "plugin"
	EvidenceSourceReflection EvidenceSource = "reflection"
)

// Short aliases are useful to adapters while the EvidenceSource-prefixed
// constants make call sites self-documenting.
const (
	SourceUser       = EvidenceSourceUser
	SourceAssistant  = EvidenceSourceAssistant
	SourceSystem     = EvidenceSourceSystem
	SourceTranscript = EvidenceSourceTranscript
	SourceMemory     = EvidenceSourceMemory
	SourceTool       = EvidenceSourceTool
	SourceFile       = EvidenceSourceFile
	SourceWeb        = EvidenceSourceWeb
	SourcePlugin     = EvidenceSourcePlugin
	SourceReflection = EvidenceSourceReflection
)

func (s EvidenceSource) Valid() bool {
	switch s {
	case EvidenceSourceUser, EvidenceSourceAssistant, EvidenceSourceSystem,
		EvidenceSourceTranscript, EvidenceSourceMemory, EvidenceSourceTool,
		EvidenceSourceFile, EvidenceSourceWeb, EvidenceSourcePlugin,
		EvidenceSourceReflection:
		return true
	default:
		return false
	}
}

// EvidenceTrust is an assertion made by the adapter about provenance. The
// reflection package never promotes external content merely because it says
// it is trusted: external sources still require explicit user confirmation.
type EvidenceTrust string

const (
	EvidenceTrusted   EvidenceTrust = "trusted"
	EvidenceUntrusted EvidenceTrust = "untrusted"
)

const (
	TrustTrusted   = EvidenceTrusted
	TrustUntrusted = EvidenceUntrusted
)

func (t EvidenceTrust) Valid() bool { return t == EvidenceTrusted || t == EvidenceUntrusted }
