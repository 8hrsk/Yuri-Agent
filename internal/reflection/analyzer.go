package reflection

import (
	"context"
	"encoding/json"
	"fmt"
)

// AnalysisRequest is the only request an Analyzer receives. Snapshot is a
// read-only value by contract; Engine also deep-copies maps/slices before the
// call to prevent a malicious analyzer from mutating caller-owned state.
type AnalysisRequest struct {
	Snapshot     InputSnapshot
	Budget       ReflectionBudget
	OutputSchema json.RawMessage
}

// AnalysisResponse may carry typed output from a local analyzer or raw strict
// JSON from a model adapter. Engine accepts either representation, preferring
// Proposal when Raw is empty.
type AnalysisResponse struct {
	Proposal ReflectionProposal
	Raw      json.RawMessage
	Usage    Usage
}

// Analyzer is the provider-neutral reflection analysis port. It cannot
// execute tools; all external content is already present in the typed
// read-only snapshot.
type Analyzer interface {
	Analyze(context.Context, AnalysisRequest) (AnalysisResponse, error)
}

// AnalyzerFunc adapts a function to Analyzer.
type AnalyzerFunc func(context.Context, AnalysisRequest) (AnalysisResponse, error)

func (f AnalyzerFunc) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResponse, error) {
	if f == nil {
		return AnalysisResponse{}, ErrNoAnalyzer
	}
	return f(ctx, request)
}

// ProposalAnalyzer is a convenient port for adapters that already produce a
// typed proposal and do not need to inspect the output schema/budget.
type ProposalAnalyzer interface {
	AnalyzeProposal(context.Context, InputSnapshot) (ReflectionProposal, error)
}

// ProposalAnalyzerFunc adapts a typed proposal function to Analyzer.
type ProposalAnalyzerFunc func(context.Context, InputSnapshot) (ReflectionProposal, error)

func (f ProposalAnalyzerFunc) AnalyzeProposal(ctx context.Context, snapshot InputSnapshot) (ReflectionProposal, error) {
	if f == nil {
		return ReflectionProposal{}, ErrNoAnalyzer
	}
	return f(ctx, snapshot)
}

// ProposalAnalyzerAdapter bridges the compact ProposalAnalyzer port to the
// budget-aware Analyzer interface.
type ProposalAnalyzerAdapter struct{ Source ProposalAnalyzer }

func (a ProposalAnalyzerAdapter) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResponse, error) {
	if a.Source == nil {
		return AnalysisResponse{}, ErrNoAnalyzer
	}
	proposal, err := a.Source.AnalyzeProposal(ctx, request.Snapshot)
	return AnalysisResponse{Proposal: proposal}, err
}

// ModelRequest is provider-neutral and intentionally does not contain tools,
// capabilities, credentials, or side-effect intents. A provider adapter may
// render Snapshot into its own prompt while preserving provenance envelopes.
type ModelRequest struct {
	Snapshot     InputSnapshot
	Budget       ReflectionBudget
	OutputSchema json.RawMessage
}

// ModelResponse is the bounded structured output of a model adapter.
type ModelResponse struct {
	JSON  json.RawMessage
	Usage Usage
}

// Model is the provider-neutral model port used by ModelAnalyzer.
type Model interface {
	Complete(context.Context, ModelRequest) (ModelResponse, error)
}

// ModelBackend is an architectural alias for callers that use backend
// terminology elsewhere in the application.
type ModelBackend = Model

// ModelFunc adapts a function to Model.
type ModelFunc func(context.Context, ModelRequest) (ModelResponse, error)

func (f ModelFunc) Complete(ctx context.Context, request ModelRequest) (ModelResponse, error) {
	if f == nil {
		return ModelResponse{}, ErrNoModel
	}
	return f(ctx, request)
}

// ModelAnalyzer decodes a model's strict JSON output and exposes it through
// the reflection Analyzer port.
type ModelAnalyzer struct{ Backend Model }

func NewModelAnalyzer(model Model) (*ModelAnalyzer, error) {
	if model == nil {
		return nil, ErrNoModel
	}
	return &ModelAnalyzer{Backend: model}, nil
}

func (a *ModelAnalyzer) Analyze(ctx context.Context, request AnalysisRequest) (AnalysisResponse, error) {
	if a == nil || a.Backend == nil {
		return AnalysisResponse{}, ErrNoModel
	}
	if len(request.OutputSchema) == 0 {
		request.OutputSchema = ProposalSchema()
	}
	response, err := a.Backend.Complete(ctx, ModelRequest{
		Snapshot: cloneSnapshot(request.Snapshot), Budget: request.Budget,
		OutputSchema: append(json.RawMessage(nil), request.OutputSchema...),
	})
	if err != nil {
		return AnalysisResponse{}, err
	}
	if request.Budget.MaxOutputBytes > 0 && len(response.JSON) > request.Budget.MaxOutputBytes {
		return AnalysisResponse{}, fmt.Errorf("%w: model output size %d exceeds %d bytes", ErrBudgetExceeded, len(response.JSON), request.Budget.MaxOutputBytes)
	}
	proposal, err := DecodeProposal(response.JSON)
	if err != nil {
		return AnalysisResponse{}, err
	}
	return AnalysisResponse{Proposal: proposal, Raw: append(json.RawMessage(nil), response.JSON...), Usage: response.Usage}, nil
}
