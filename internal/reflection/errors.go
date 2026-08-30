package reflection

import "errors"

var (
	// ErrInvalidSnapshot means that a reflection request is incomplete or
	// contains malformed, unverifiable input.
	ErrInvalidSnapshot = errors.New("reflection: invalid input snapshot")
	// ErrInvalidProposal means that the analyzer returned a malformed or
	// semantically inconsistent proposal.
	ErrInvalidProposal = errors.New("reflection: invalid proposal")
	// ErrSchema means that model output is not the strict reflection schema.
	ErrSchema = errors.New("reflection: schema validation failed")
	// ErrNoAnalyzer means that the reflection engine has no analyzer port.
	ErrNoAnalyzer = errors.New("reflection: analyzer is not configured")
	// ErrNoModel means that the model-backed analyzer has no model port.
	ErrNoModel = errors.New("reflection: model is not configured")
	// ErrBudgetExceeded means that a reflection run exceeded an explicit
	// input, output, token, evidence, or wall-clock budget.
	ErrBudgetExceeded = errors.New("reflection: run budget exceeded")
	// ErrInsufficientEvidence means that a proposed change does not have the
	// configured minimum number/weight of independent evidence references.
	ErrInsufficientEvidence = errors.New("reflection: insufficient evidence")
	// ErrDeltaExceeded means that a proposed scalar change is larger than the
	// configured bounded delta.
	ErrDeltaExceeded = errors.New("reflection: maximum delta exceeded")
	// ErrOutOfRange means that applying a proposed change would leave a trait or
	// state dimension outside its configured range.
	ErrOutOfRange = errors.New("reflection: value outside configured range")
	// ErrPinnedTrait means that a proposal attempts to alter a pinned persona
	// trait.
	ErrPinnedTrait = errors.New("reflection: trait is pinned")
	// ErrForbiddenMutation means that a proposal tries to modify an immutable
	// security/identity boundary or embeds an instruction that could do so.
	ErrForbiddenMutation = errors.New("reflection: forbidden mutation")
	// ErrUntrustedEvidence means that unconfirmed external data was selected as
	// the basis for a mutable persona/identity change.
	ErrUntrustedEvidence = errors.New("reflection: untrusted evidence cannot mutate identity")
	// ErrCooldown means that the profile's reflection cooldown has not elapsed.
	ErrCooldown = errors.New("reflection: cooldown is active")
	// ErrProfileBusy is returned by TryRun when another reflection is active for
	// the same profile.
	ErrProfileBusy = errors.New("reflection: profile already has an active run")
	// ErrOpinionLimit is returned when a relationship opinion would exceed the
	// configured count or content bound.
	ErrOpinionLimit = errors.New("reflection: subjective opinion limit exceeded")
)
