package desktop

import (
	"context"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

type PeerRelationshipListInput struct {
	Limit int `json:"limit,omitempty"`
}
type PeerRelationshipInput struct {
	PeerAgentID string `json:"peerAgentId"`
}
type PeerRelationshipRollbackInput struct {
	PeerAgentID string `json:"peerAgentId"`
	VersionID   string `json:"versionId"`
}

type PeerRelationshipView struct {
	ObserverAgentID  string                  `json:"observerAgentId"`
	PeerAgentID      string                  `json:"peerAgentId"`
	PeerName         string                  `json:"peerName"`
	RelationshipID   string                  `json:"relationshipId"`
	Version          uint64                  `json:"version"`
	CurrentVersionID string                  `json:"currentVersionId"`
	Summary          string                  `json:"summary"`
	Dimensions       map[string]float64      `json:"dimensions"`
	Opinions         []SubjectiveOpinionView `json:"opinions"`
	Reason           string                  `json:"reason,omitempty"`
	Evidence         []PersonaEvidenceView   `json:"evidence,omitempty"`
	UpdatedAt        string                  `json:"updatedAt"`
}

type PeerRelationshipVersionView struct {
	ID          string                  `json:"id"`
	Version     uint64                  `json:"version"`
	ParentID    string                  `json:"parentId,omitempty"`
	Operation   string                  `json:"operation"`
	Summary     string                  `json:"summary"`
	Dimensions  map[string]float64      `json:"dimensions"`
	Opinions    []SubjectiveOpinionView `json:"opinions"`
	Reason      string                  `json:"reason"`
	Evidence    []PersonaEvidenceView   `json:"evidence,omitempty"`
	AuthorRunID string                  `json:"authorRunId,omitempty"`
	CreatedAt   string                  `json:"createdAt"`
}

type PeerRelationshipDetailView struct {
	Relationship PeerRelationshipView          `json:"relationship"`
	Versions     []PeerRelationshipVersionView `json:"versions"`
}

func (b *Bridge) ListPeerRelationships(input PeerRelationshipListInput) ([]PeerRelationshipView, error) {
	ctx, cancel := b.context()
	defer cancel()
	observerID := b.personaProfileID()
	links, err := b.repositories.PeerSocial.ListRelationships(ctx, observerID, input.Limit)
	if err != nil {
		return nil, err
	}
	relationshipIDs := make([]domain.ID, 0, len(links))
	peerIDs := make([]domain.ID, 0, len(links))
	for _, link := range links {
		relationshipIDs = append(relationshipIDs, link.RelationshipID)
		peerIDs = append(peerIDs, link.SubjectAgentID)
	}
	states, err := b.repositories.Relationship.ListByIDs(ctx, relationshipIDs)
	if err != nil {
		return nil, err
	}
	profiles, err := b.repositories.Agents.ListByIDs(ctx, peerIDs)
	if err != nil {
		return nil, err
	}
	result := make([]PeerRelationshipView, 0, len(links))
	for _, link := range links {
		state, stateOK := states[link.RelationshipID]
		profile, profileOK := profiles[link.SubjectAgentID]
		if !stateOK || !profileOK {
			return nil, domain.ErrNotFound
		}
		result = append(result, peerRelationshipView(observerID, profile, state))
	}
	return result, nil
}

func (b *Bridge) GetPeerRelationship(input PeerRelationshipInput) (PeerRelationshipDetailView, error) {
	ctx, cancel := b.context()
	defer cancel()
	observerID := b.personaProfileID()
	return b.peerRelationshipDetail(ctx, observerID, domain.ID(strings.TrimSpace(input.PeerAgentID)))
}

func (b *Bridge) RollbackPeerRelationship(input PeerRelationshipRollbackInput) (PeerRelationshipDetailView, error) {
	ctx, cancel := b.context()
	defer cancel()
	observerID := b.personaProfileID()
	peerID := domain.ID(strings.TrimSpace(input.PeerAgentID))
	versionID := strings.TrimSpace(input.VersionID)
	if peerID.Empty() || versionID == "" || peerID == observerID {
		return PeerRelationshipDetailView{}, domain.ErrInvalidArgument
	}
	current, err := b.repositories.PeerSocial.GetRelationship(ctx, observerID, peerID)
	if err != nil {
		return PeerRelationshipDetailView{}, err
	}
	history, err := b.repositories.Relationship.ListVersions(ctx, current.ID, 100)
	if err != nil {
		return PeerRelationshipDetailView{}, err
	}
	var target uint64
	for _, record := range history {
		if record.RevisionID.String() == versionID || record.Relationship.RevisionID.String() == versionID {
			target = record.Relationship.Version
			break
		}
	}
	if target == 0 {
		return PeerRelationshipDetailView{}, domain.ErrNotFound
	}
	if _, err := b.repositories.Relationship.Rollback(ctx, current.ID, current.Version, target, "Владелец откатил мнение агента о peer", time.Now().UTC()); err != nil {
		return PeerRelationshipDetailView{}, err
	}
	return b.peerRelationshipDetail(ctx, observerID, peerID)
}

func (b *Bridge) ResetPeerRelationship(input PeerRelationshipInput) (PeerRelationshipDetailView, error) {
	ctx, cancel := b.context()
	defer cancel()
	observerID := b.personaProfileID()
	peerID := domain.ID(strings.TrimSpace(input.PeerAgentID))
	if peerID.Empty() || peerID == observerID {
		return PeerRelationshipDetailView{}, domain.ErrInvalidArgument
	}
	current, err := b.repositories.PeerSocial.GetRelationship(ctx, observerID, peerID)
	if err != nil {
		return PeerRelationshipDetailView{}, err
	}
	if _, err := b.repositories.Relationship.Reset(ctx, current.ID, current.Version, "Владелец сбросил мнение агента о peer", time.Now().UTC()); err != nil {
		return PeerRelationshipDetailView{}, err
	}
	return b.peerRelationshipDetail(ctx, observerID, peerID)
}

func (b *Bridge) peerRelationshipDetail(ctx context.Context, observerID, peerID domain.ID) (PeerRelationshipDetailView, error) {
	if peerID.Empty() || peerID == observerID {
		return PeerRelationshipDetailView{}, domain.ErrInvalidArgument
	}
	state, err := b.repositories.PeerSocial.GetRelationship(ctx, observerID, peerID)
	if err != nil {
		return PeerRelationshipDetailView{}, err
	}
	profile, err := b.repositories.Agents.Get(ctx, peerID)
	if err != nil {
		return PeerRelationshipDetailView{}, err
	}
	history, err := b.repositories.Relationship.ListVersions(ctx, state.ID, 100)
	if err != nil {
		return PeerRelationshipDetailView{}, err
	}
	versions := make([]PeerRelationshipVersionView, 0, len(history))
	for _, record := range history {
		item := record.Relationship
		versions = append(versions, PeerRelationshipVersionView{ID: firstDomainID(record.RevisionID, item.RevisionID).String(), Version: item.Version, ParentID: firstDomainID(record.ParentID, item.ParentID).String(), Operation: string(firstRelationshipOperation(record.Operation, item.Operation)), Summary: item.Summary, Dimensions: copyFloatMap(item.Dimensions), Opinions: opinionViews(item.Opinions), Reason: firstNonEmpty(record.Reason, item.Reason, "Версия отношения"), Evidence: evidenceViews(firstEvidence(record.Evidence, item.Evidence)), AuthorRunID: firstDomainID(record.AuthorRunID, item.AuthorRunID).String(), CreatedAt: item.UpdatedAt.UTC().Format(time.RFC3339Nano)})
	}
	return PeerRelationshipDetailView{Relationship: peerRelationshipView(observerID, profile, state), Versions: versions}, nil
}

func peerRelationshipView(observerID domain.ID, peer domain.AgentProfile, state domain.RelationshipState) PeerRelationshipView {
	return PeerRelationshipView{ObserverAgentID: observerID.String(), PeerAgentID: peer.ID.String(), PeerName: peer.Name, RelationshipID: state.ID.String(), Version: state.Version, CurrentVersionID: state.RevisionID.String(), Summary: state.Summary, Dimensions: copyFloatMap(state.Dimensions), Opinions: opinionViews(state.Opinions), Reason: state.Reason, Evidence: evidenceViews(state.Evidence), UpdatedAt: state.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func firstRelationshipOperation(values ...domain.RelationshipOperation) domain.RelationshipOperation {
	for _, value := range values {
		if value.Valid() {
			return value
		}
	}
	return domain.RelationshipOperationUpdate
}
