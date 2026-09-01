package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// PeerDialogueRepository persists bounded background exchanges between two
// named agents. The aggregate owns its initial message and subsequent turns;
// callers cannot create a dialogue row without its sequence-zero opener.
type PeerDialogueRepository struct {
	db *sql.DB
}

// PeerDialogueMessageRepository reads the immutable turn log of a peer
// dialogue. Every public read requires one of the two participant IDs.
type PeerDialogueMessageRepository struct {
	db *sql.DB
}

func NewPeerDialogueRepository(database *sql.DB) *PeerDialogueRepository {
	return &PeerDialogueRepository{db: database}
}

func NewPeerDialogueMessageRepository(database *sql.DB) *PeerDialogueMessageRepository {
	return &PeerDialogueMessageRepository{db: database}
}

// CreatePeerDialogueWithMessage atomically inserts the dialogue and its
// sequence-zero initiator message. SQLite foreign keys and triggers repeat the
// ownership checks so a direct adapter call cannot create an orphaned or
// cross-agent exchange.
func (repositories *Repositories) CreatePeerDialogueWithMessage(ctx context.Context, dialogue domain.PeerDialogue, initial domain.PeerDialogueMessage) error {
	if repositories == nil || repositories.PeerDialogues == nil || repositories.PeerDialogueMessages == nil {
		return fmt.Errorf("%w: peer dialogue repositories are unavailable", domain.ErrInvalidArgument)
	}
	return createPeerDialogueWithMessage(ctx, repositories.PeerDialogues.db, dialogue, initial)
}

// Create is a convenience form for callers that already hold the dialogue
// repository. It preserves the same aggregate invariant as the Repositories
// method above.
func (r *PeerDialogueRepository) Create(ctx context.Context, dialogue domain.PeerDialogue, initial domain.PeerDialogueMessage) error {
	if r == nil {
		return fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	return createPeerDialogueWithMessage(ctx, r.db, dialogue, initial)
}

func createPeerDialogueWithMessage(ctx context.Context, database *sql.DB, dialogue domain.PeerDialogue, initial domain.PeerDialogueMessage) error {
	if err := requireDatabase(database); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := validatePeerDialogueForCreate(dialogue, initial); err != nil {
		return err
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin create peer dialogue", err)
	}
	defer tx.Rollback()
	if err := insertPeerDialogueTx(ctx, tx, dialogue); err != nil {
		return err
	}
	if err := insertPeerDialogueMessageTx(ctx, tx, initial); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit create peer dialogue", err)
	}
	return nil
}

func validatePeerDialogueForCreate(dialogue domain.PeerDialogue, initial domain.PeerDialogueMessage) error {
	if err := dialogue.Validate(); err != nil {
		return err
	}
	if dialogue.Status != domain.PeerDialogueQueued || dialogue.Version != 1 || dialogue.TurnCount != 0 || dialogue.TokensUsed != 0 || !dialogue.StartedAt.IsZero() || !dialogue.FinishedAt.IsZero() {
		return fmt.Errorf("%w: new peer dialogue must be queued at version one with no turns", domain.ErrInvalidArgument)
	}
	if err := initial.Validate(); err != nil {
		return err
	}
	if initial.DialogueID != dialogue.ID || initial.Sequence != 0 ||
		initial.SenderAgentID != dialogue.InitiatorAgentID ||
		initial.RecipientAgentID != dialogue.PeerAgentID ||
		initial.SourceRunID != dialogue.TriggerRunID {
		return fmt.Errorf("%w: initial peer dialogue message must be the initiator sequence-zero opener", domain.ErrInvalidArgument)
	}
	return nil
}

func insertPeerDialogueTx(ctx context.Context, tx *sql.Tx, dialogue domain.PeerDialogue) error {
	createdAt, err := timeValue(dialogue.CreatedAt)
	if err != nil {
		return err
	}
	updatedAt, err := timeValue(dialogue.UpdatedAt)
	if err != nil {
		return err
	}
	expiresAt, err := timeValue(dialogue.ExpiresAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO peer_dialogues(
			id, initiator_agent_id, peer_agent_id, trigger_run_id, trigger_kind, trigger_reason, pair_key, purpose, status,
			max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
			idempotency_key, request_hash, failure, failure_kind, failure_retryable, failure_retry_after_seconds,
			version, created_at, updated_at, started_at, finished_at, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(dialogue.ID), string(dialogue.InitiatorAgentID), string(dialogue.PeerAgentID), string(dialogue.TriggerRunID),
		string(dialogue.TriggerKind), strings.TrimSpace(dialogue.TriggerReason), dialogue.PairKey, strings.TrimSpace(dialogue.Purpose), string(dialogue.Status),
		dialogue.Budget.MaxTurns, dialogue.Budget.MaxTokens, dialogue.Budget.MaxDurationSeconds, dialogue.Budget.CooldownSeconds,
		dialogue.TurnCount, dialogue.TokensUsed, strings.TrimSpace(dialogue.IdempotencyKey), strings.TrimSpace(dialogue.RequestHash),
		dialogue.Failure, string(dialogue.FailureInfo.Kind), dialogue.FailureInfo.Retryable, dialogue.FailureInfo.RetryAfterSeconds,
		dialogue.Version, createdAt, updatedAt, nullableTimeValue(dialogue.StartedAt), nullableTimeValue(dialogue.FinishedAt), expiresAt)
	return wrappedSQLError("create peer dialogue", err)
}

func insertPeerDialogueMessageTx(ctx context.Context, tx *sql.Tx, message domain.PeerDialogueMessage) error {
	createdAt, err := timeValue(message.CreatedAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO peer_dialogue_messages(
			id, dialogue_id, sequence, sender_agent_id, recipient_agent_id, source_run_id, content, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(message.ID), string(message.DialogueID), message.Sequence, string(message.SenderAgentID),
		string(message.RecipientAgentID), string(message.SourceRunID), strings.TrimSpace(message.Content), createdAt)
	return wrappedSQLError("create peer dialogue message", err)
}

func (r *PeerDialogueRepository) Get(ctx context.Context, id domain.ID) (domain.PeerDialogue, error) {
	if r == nil {
		return domain.PeerDialogue{}, fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := requireDatabase(r.db); err != nil {
		return domain.PeerDialogue{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.PeerDialogue{}, err
	}
	if id.Empty() {
		return domain.PeerDialogue{}, fmt.Errorf("%w: peer dialogue id is required", domain.ErrInvalidArgument)
	}
	return getPeerDialogue(ctx, r.db, string(id))
}

// GetForParticipant hides whether a dialogue exists when the caller is not
// one of its two named participants.
func (r *PeerDialogueRepository) GetForParticipant(ctx context.Context, participantAgentID, dialogueID domain.ID) (domain.PeerDialogue, error) {
	if participantAgentID.Empty() || dialogueID.Empty() {
		return domain.PeerDialogue{}, fmt.Errorf("%w: participant and dialogue ids are required", domain.ErrInvalidArgument)
	}
	dialogue, err := r.Get(ctx, dialogueID)
	if err != nil {
		return domain.PeerDialogue{}, err
	}
	if dialogue.InitiatorAgentID != participantAgentID && dialogue.PeerAgentID != participantAgentID {
		return domain.PeerDialogue{}, domain.ErrNotFound
	}
	return dialogue, nil
}

// ListByParticipant returns only dialogues in which participantAgentID is a
// named participant. It never exposes the other agent's unrelated history.
func (r *PeerDialogueRepository) ListByParticipant(ctx context.Context, participantAgentID domain.ID, window ...int) ([]domain.PeerDialogue, error) {
	if participantAgentID.Empty() {
		return nil, fmt.Errorf("%w: participant agent id is required", domain.ErrInvalidArgument)
	}
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit, offset, err := listWindow("peer dialogue", window)
	if err != nil {
		return nil, err
	}
	query := peerDialogueSelect + `
		WHERE initiator_agent_id = ? OR peer_agent_id = ?
		ORDER BY created_at DESC, id DESC`
	args := []any{string(participantAgentID), string(participantAgentID)}
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list peer dialogues", err)
	}
	defer rows.Close()
	result := make([]domain.PeerDialogue, 0)
	for rows.Next() {
		item, scanErr := scanPeerDialogue(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate peer dialogues", err)
	}
	return result, nil
}

// ListForParticipant is an explicit alias for callers that prefer the
// participant-first naming used by the scoped Get method.
func (r *PeerDialogueRepository) ListForParticipant(ctx context.Context, participantAgentID domain.ID, window ...int) ([]domain.PeerDialogue, error) {
	return r.ListByParticipant(ctx, participantAgentID, window...)
}

// ListCompleted returns a bounded newest-first reconciliation window. It is
// intentionally installation-local rather than participant-scoped: startup
// uses it to repair missing private episodic projections for both parties.
func (r *PeerDialogueRepository) ListCompleted(ctx context.Context, window ...int) ([]domain.PeerDialogue, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit, offset, err := listWindow("completed peer dialogue", window)
	if err != nil {
		return nil, err
	}
	query := peerDialogueSelect + `
		WHERE status = 'completed'
		ORDER BY updated_at DESC, id DESC`
	args := make([]any, 0, 2)
	query, args = appendWindow(query, args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list completed peer dialogues", err)
	}
	defer rows.Close()
	result := make([]domain.PeerDialogue, 0)
	for rows.Next() {
		item, scanErr := scanPeerDialogue(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate completed peer dialogues", err)
	}
	return result, nil
}

// ListAutonomousByInitiator returns a bounded newest-first ledger used to
// rebuild the autonomous-trigger rate policy after restart.
func (r *PeerDialogueRepository) ListAutonomousByInitiator(ctx context.Context, initiatorAgentID domain.ID, since time.Time, limit int) ([]domain.PeerDialogue, error) {
	if r == nil || r.db == nil || initiatorAgentID.Empty() || since.IsZero() {
		return nil, fmt.Errorf("%w: autonomous peer dialogue scope is required", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, peerDialogueSelect+`
		WHERE initiator_agent_id = ? AND trigger_kind = ? AND created_at >= ?
		ORDER BY created_at DESC, id DESC LIMIT ?`, initiatorAgentID.String(), string(domain.PeerDialogueTriggerAutonomous), formatTime(since), limit)
	if err != nil {
		return nil, wrappedSQLError("list autonomous peer dialogues", err)
	}
	defer rows.Close()
	result := make([]domain.PeerDialogue, 0)
	for rows.Next() {
		dialogue, scanErr := scanPeerDialogue(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, dialogue)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate autonomous peer dialogues", err)
	}
	return result, nil
}

// FindByIdempotencyKey is scoped to the initiating agent and the root run,
// preventing a retry from another agent or another trigger run from reusing a
// previous exchange.
func (r *PeerDialogueRepository) FindByIdempotencyKey(ctx context.Context, initiatorAgentID, triggerRunID domain.ID, key string) (domain.PeerDialogue, error) {
	if initiatorAgentID.Empty() || triggerRunID.Empty() || strings.TrimSpace(key) == "" {
		return domain.PeerDialogue{}, fmt.Errorf("%w: peer dialogue idempotency scope is required", domain.ErrInvalidArgument)
	}
	if r == nil || r.db == nil {
		return domain.PeerDialogue{}, fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return domain.PeerDialogue{}, err
	}
	return scanPeerDialogue(r.db.QueryRowContext(ctx,
		peerDialogueSelect+` WHERE initiator_agent_id = ? AND trigger_run_id = ? AND idempotency_key = ?`,
		string(initiatorAgentID), string(triggerRunID), strings.TrimSpace(key)))
}

// HasByTriggerRun prevents one foreground root run from opening both an
// explicit tool dialogue and an additional autonomous dialogue afterwards.
func (r *PeerDialogueRepository) HasByTriggerRun(ctx context.Context, initiatorAgentID, triggerRunID domain.ID) (bool, error) {
	if r == nil || r.db == nil || initiatorAgentID.Empty() || triggerRunID.Empty() {
		return false, fmt.Errorf("%w: peer dialogue trigger scope is required", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	var exists int
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM peer_dialogues WHERE initiator_agent_id = ? AND trigger_run_id = ?)`, initiatorAgentID.String(), triggerRunID.String()).Scan(&exists)
	if err != nil {
		return false, wrappedSQLError("check peer dialogue trigger run", err)
	}
	return exists == 1, nil
}

// HasRecentPair reports whether either agent has opened an exchange for this
// unordered pair at or after since. Terminal rows are included deliberately:
// a failed/expired attempt still consumes the configured cooldown window.
func (r *PeerDialogueRepository) HasRecentPair(ctx context.Context, pairKey string, since time.Time) (bool, error) {
	if strings.TrimSpace(pairKey) == "" || since.IsZero() {
		return false, fmt.Errorf("%w: pair key and since timestamp are required", domain.ErrInvalidArgument)
	}
	if r == nil || r.db == nil {
		return false, fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return false, err
	}
	var exists int
	err := r.db.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM peer_dialogues
			WHERE pair_key = ? AND created_at >= ?
		)`, strings.TrimSpace(pairKey), formatTime(since)).Scan(&exists)
	if err != nil {
		return false, wrappedSQLError("check peer dialogue cooldown", err)
	}
	return exists == 1, nil
}

// interruptedDialogue is the minimal projection RecoverInterrupted needs to
// close a non-terminal exchange: which row, what it is being moved away from,
// and the version its update must still match.
type interruptedDialogue struct {
	id      string
	status  domain.PeerDialogueStatus
	version uint64
}

// selectInterruptedPeerDialoguesTx materializes the whole candidate set before
// returning. That is not an optimization: the pool is a single connection and
// the caller updates rows on this same transaction, so the cursor has to be
// closed before the first UPDATE rather than held open across it. Doing the
// read in its own function makes that ordering structural, and lets the cursor
// be released by defer on every path instead of by a Close at each exit.
func selectInterruptedPeerDialoguesTx(ctx context.Context, tx *sql.Tx) ([]interruptedDialogue, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status, version
		FROM peer_dialogues
		WHERE status IN ('queued', 'running', 'cancelling')
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, wrappedSQLError("list interrupted peer dialogues", err)
	}
	defer rows.Close()
	items := make([]interruptedDialogue, 0)
	for rows.Next() {
		var item interruptedDialogue
		var status string
		if err := rows.Scan(&item.id, &status, &item.version); err != nil {
			return nil, wrappedSQLError("scan interrupted peer dialogue", err)
		}
		item.status = domain.PeerDialogueStatus(status)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate interrupted peer dialogues", err)
	}
	// Closed explicitly as well as by the defer above so a close failure is
	// still reported rather than discarded; sql.Rows.Close is idempotent.
	if err := rows.Close(); err != nil {
		return nil, wrappedSQLError("close interrupted peer dialogues", err)
	}
	return items, nil
}

// RecoverInterrupted closes non-terminal exchanges left by a process crash
// or an unclean shutdown. A queued exchange never started, so it is marked
// cancelled; a running/cancelling exchange is marked failed with the bounded
// operator-supplied reason. The operation is one transaction and each update
// retains a version predicate so a concurrent worker cannot be overwritten.
func (r *PeerDialogueRepository) RecoverInterrupted(ctx context.Context, now time.Time, reason string) error {
	if r == nil {
		return fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if now.IsZero() {
		return fmt.Errorf("%w: recovery timestamp is required", domain.ErrInvalidArgument)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "interrupted during application shutdown"
	}
	if len(reason) > 1024 || strings.ContainsRune(reason, '\x00') {
		return fmt.Errorf("%w: recovery reason exceeds 1 KiB", domain.ErrInvalidArgument)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin recover peer dialogues", err)
	}
	defer tx.Rollback()
	items, err := selectInterruptedPeerDialoguesTx(ctx, tx)
	if err != nil {
		return err
	}
	updatedAt := formatTime(now)
	for _, item := range items {
		nextStatus := domain.PeerDialogueCancelled
		failure := ""
		if item.status == domain.PeerDialogueRunning || item.status == domain.PeerDialogueCancelling {
			nextStatus = domain.PeerDialogueFailed
			failure = reason
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE peer_dialogues
			SET status = ?, failure = ?, version = ?, updated_at = ?, finished_at = ?
			WHERE id = ? AND status = ? AND version = ?`,
			string(nextStatus), failure, item.version+1, updatedAt, updatedAt,
			item.id, string(item.status), item.version)
		if err != nil {
			return wrappedSQLError("recover peer dialogue", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return wrappedSQLError("count recovered peer dialogue", err)
		}
		if affected != 1 {
			return domain.ErrConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit recover peer dialogues", err)
	}
	return nil
}

// Save performs an optimistic update. Identity, pair, trigger and
// idempotency fields are immutable after creation; lifecycle and budget data
// are updated only when Version is exactly the next value.
func (r *PeerDialogueRepository) Save(ctx context.Context, dialogue domain.PeerDialogue) error {
	if r == nil {
		return fmt.Errorf("%w: peer dialogue repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := requireDatabase(r.db); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := dialogue.Validate(); err != nil {
		return err
	}
	if dialogue.Version < 2 {
		return fmt.Errorf("%w: peer dialogue version must be greater than one when saving", domain.ErrInvalidArgument)
	}
	current, err := r.Get(ctx, dialogue.ID)
	if err != nil {
		return err
	}
	if !samePeerDialogueIdentity(current, dialogue) {
		return domain.ErrConflict
	}
	if dialogue.Version != current.Version+1 {
		return domain.ErrConflict
	}
	return savePeerDialogue(ctx, r.db, dialogue, current.Version)
}

func samePeerDialogueIdentity(left, right domain.PeerDialogue) bool {
	return left.InitiatorAgentID == right.InitiatorAgentID &&
		left.PeerAgentID == right.PeerAgentID &&
		left.TriggerRunID == right.TriggerRunID &&
		left.PairKey == right.PairKey &&
		left.IdempotencyKey == right.IdempotencyKey &&
		left.RequestHash == right.RequestHash
}

func savePeerDialogue(ctx context.Context, database *sql.DB, dialogue domain.PeerDialogue, expectedVersion uint64) error {
	updatedAt, err := timeValue(dialogue.UpdatedAt)
	if err != nil {
		return err
	}
	result, err := database.ExecContext(ctx, `
		UPDATE peer_dialogues SET
			purpose = ?, status = ?, max_turns = ?, max_tokens = ?, max_duration_seconds = ?, cooldown_seconds = ?,
			turn_count = ?, tokens_used = ?, failure = ?, failure_kind = ?, failure_retryable = ?, failure_retry_after_seconds = ?,
			version = ?, updated_at = ?, started_at = ?, finished_at = ?, expires_at = ?
		WHERE id = ? AND version = ?`,
		strings.TrimSpace(dialogue.Purpose), string(dialogue.Status), dialogue.Budget.MaxTurns, dialogue.Budget.MaxTokens,
		dialogue.Budget.MaxDurationSeconds, dialogue.Budget.CooldownSeconds, dialogue.TurnCount, dialogue.TokensUsed,
		dialogue.Failure, string(dialogue.FailureInfo.Kind), dialogue.FailureInfo.Retryable, dialogue.FailureInfo.RetryAfterSeconds,
		dialogue.Version, updatedAt, nullableTimeValue(dialogue.StartedAt), nullableTimeValue(dialogue.FinishedAt),
		formatTime(dialogue.ExpiresAt), string(dialogue.ID), expectedVersion)
	if err != nil {
		return wrappedSQLError("save peer dialogue", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count saved peer dialogue", err)
	}
	if count == 1 {
		return nil
	}
	if _, err := (&PeerDialogueRepository{db: database}).Get(ctx, dialogue.ID); err != nil {
		return err
	}
	return domain.ErrConflict
}

// AppendPeerDialogueTurn atomically updates an already-mutated dialogue and
// appends its next generated message. The caller must have called
// PeerDialogue.RecordTurn first; the optimistic version and turn count make
// stale/replayed appends fail without leaving half an aggregate persisted.
func (repositories *Repositories) AppendPeerDialogueTurn(ctx context.Context, dialogue domain.PeerDialogue, message domain.PeerDialogueMessage) error {
	if repositories == nil || repositories.PeerDialogues == nil || repositories.PeerDialogueMessages == nil {
		return fmt.Errorf("%w: peer dialogue repositories are unavailable", domain.ErrInvalidArgument)
	}
	return appendPeerDialogueTurn(ctx, repositories.PeerDialogues.db, dialogue, message)
}

// AppendGeneratedTurn is a descriptive alias for the aggregate operation.
func (repositories *Repositories) AppendGeneratedTurn(ctx context.Context, dialogue domain.PeerDialogue, message domain.PeerDialogueMessage) error {
	return repositories.AppendPeerDialogueTurn(ctx, dialogue, message)
}

func appendPeerDialogueTurn(ctx context.Context, database *sql.DB, dialogue domain.PeerDialogue, message domain.PeerDialogueMessage) error {
	if err := requireDatabase(database); err != nil {
		return err
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	if err := dialogue.Validate(); err != nil {
		return err
	}
	if dialogue.Status != domain.PeerDialogueRunning || dialogue.Version < 2 || dialogue.TurnCount < 1 {
		return fmt.Errorf("%w: peer dialogue must be running with a generated turn", domain.ErrInvalidArgument)
	}
	if err := message.Validate(); err != nil {
		return err
	}
	if err := validateGeneratedPeerMessage(dialogue, message); err != nil {
		return err
	}

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return wrappedSQLError("begin append peer dialogue turn", err)
	}
	defer tx.Rollback()
	current, err := getPeerDialogueTx(ctx, tx, dialogue.ID.String())
	if err != nil {
		return err
	}
	if !samePeerDialogueIdentity(current, dialogue) || current.Version+1 != dialogue.Version ||
		current.TurnCount+1 != dialogue.TurnCount || current.TokensUsed > dialogue.TokensUsed ||
		current.Status != domain.PeerDialogueRunning {
		return domain.ErrConflict
	}
	if err := updatePeerDialogueTx(ctx, tx, dialogue, current.Version); err != nil {
		return err
	}
	if err := insertPeerDialogueMessageTx(ctx, tx, message); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrappedSQLError("commit append peer dialogue turn", err)
	}
	return nil
}

func validateGeneratedPeerMessage(dialogue domain.PeerDialogue, message domain.PeerDialogueMessage) error {
	if message.DialogueID != dialogue.ID || message.Sequence != dialogue.TurnCount {
		return fmt.Errorf("%w: generated message must match the next dialogue sequence", domain.ErrInvalidArgument)
	}
	expectedSender, expectedRecipient := dialogue.InitiatorAgentID, dialogue.PeerAgentID
	if message.Sequence%2 == 1 {
		expectedSender, expectedRecipient = expectedRecipient, expectedSender
	}
	if message.SenderAgentID != expectedSender || message.RecipientAgentID != expectedRecipient {
		return fmt.Errorf("%w: generated message participants do not alternate", domain.ErrInvalidArgument)
	}
	return nil
}

func updatePeerDialogueTx(ctx context.Context, tx *sql.Tx, dialogue domain.PeerDialogue, expectedVersion uint64) error {
	updatedAt, err := timeValue(dialogue.UpdatedAt)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE peer_dialogues SET
			purpose = ?, status = ?, max_turns = ?, max_tokens = ?, max_duration_seconds = ?, cooldown_seconds = ?,
			turn_count = ?, tokens_used = ?, failure = ?, failure_kind = ?, failure_retryable = ?, failure_retry_after_seconds = ?,
			version = ?, updated_at = ?, started_at = ?, finished_at = ?, expires_at = ?
		WHERE id = ? AND version = ?`,
		strings.TrimSpace(dialogue.Purpose), string(dialogue.Status), dialogue.Budget.MaxTurns, dialogue.Budget.MaxTokens,
		dialogue.Budget.MaxDurationSeconds, dialogue.Budget.CooldownSeconds, dialogue.TurnCount, dialogue.TokensUsed,
		dialogue.Failure, string(dialogue.FailureInfo.Kind), dialogue.FailureInfo.Retryable, dialogue.FailureInfo.RetryAfterSeconds,
		dialogue.Version, updatedAt, nullableTimeValue(dialogue.StartedAt), nullableTimeValue(dialogue.FinishedAt),
		formatTime(dialogue.ExpiresAt), string(dialogue.ID), expectedVersion)
	if err != nil {
		return wrappedSQLError("update peer dialogue", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrappedSQLError("count updated peer dialogue", err)
	}
	if count != 1 {
		return domain.ErrConflict
	}
	return nil
}

// peerDialogueSelect lists the full record so the single-row reads and
// ListByParticipant share one scanner and one round-trip per query.
const peerDialogueSelect = `
	SELECT id, initiator_agent_id, peer_agent_id, trigger_run_id, trigger_kind, trigger_reason, pair_key, purpose, status,
	       max_turns, max_tokens, max_duration_seconds, cooldown_seconds, turn_count, tokens_used,
	       idempotency_key, request_hash, failure, failure_kind, failure_retryable, failure_retry_after_seconds,
	       version, created_at, updated_at, started_at, finished_at, expires_at
	FROM peer_dialogues`

func getPeerDialogue(ctx context.Context, database *sql.DB, id string) (domain.PeerDialogue, error) {
	return scanPeerDialogue(database.QueryRowContext(ctx, peerDialogueSelect+` WHERE id = ?`, id))
}

func getPeerDialogueTx(ctx context.Context, tx *sql.Tx, id string) (domain.PeerDialogue, error) {
	return scanPeerDialogue(tx.QueryRowContext(ctx, peerDialogueSelect+` WHERE id = ?`, id))
}

type peerDialogueScanner interface {
	Scan(dest ...any) error
}

func scanPeerDialogue(scanner peerDialogueScanner) (domain.PeerDialogue, error) {
	var dialogue domain.PeerDialogue
	var (
		id, initiatorID, peerID, triggerRunID, triggerKind, status string
		createdAt, updatedAt, expiresAt                            string
		startedAt, finishedAt                                      sql.NullString
	)
	if err := scanner.Scan(
		&id, &initiatorID, &peerID, &triggerRunID, &triggerKind, &dialogue.TriggerReason, &dialogue.PairKey, &dialogue.Purpose, &status,
		&dialogue.Budget.MaxTurns, &dialogue.Budget.MaxTokens, &dialogue.Budget.MaxDurationSeconds, &dialogue.Budget.CooldownSeconds,
		&dialogue.TurnCount, &dialogue.TokensUsed, &dialogue.IdempotencyKey, &dialogue.RequestHash, &dialogue.Failure,
		&dialogue.FailureInfo.Kind, &dialogue.FailureInfo.Retryable, &dialogue.FailureInfo.RetryAfterSeconds,
		&dialogue.Version, &createdAt, &updatedAt, &startedAt, &finishedAt, &expiresAt); err != nil {
		return domain.PeerDialogue{}, wrappedSQLError("scan peer dialogue", err)
	}
	dialogue.ID = domain.ID(id)
	dialogue.InitiatorAgentID = domain.ID(initiatorID)
	dialogue.PeerAgentID = domain.ID(peerID)
	dialogue.TriggerRunID = domain.ID(triggerRunID)
	dialogue.TriggerKind = domain.PeerDialogueTriggerKind(triggerKind)
	dialogue.Status = domain.PeerDialogueStatus(status)
	var err error
	if dialogue.CreatedAt, err = scanTime(createdAt); err != nil {
		return domain.PeerDialogue{}, err
	}
	if dialogue.UpdatedAt, err = scanTime(updatedAt); err != nil {
		return domain.PeerDialogue{}, err
	}
	if dialogue.ExpiresAt, err = scanTime(expiresAt); err != nil {
		return domain.PeerDialogue{}, err
	}
	if dialogue.StartedAt, err = scanNullableTime(startedAt); err != nil {
		return domain.PeerDialogue{}, err
	}
	if dialogue.FinishedAt, err = scanNullableTime(finishedAt); err != nil {
		return domain.PeerDialogue{}, err
	}
	if err := dialogue.Validate(); err != nil {
		return domain.PeerDialogue{}, err
	}
	return dialogue, nil
}

func (r *PeerDialogueMessageRepository) Get(ctx context.Context, id domain.ID) (domain.PeerDialogueMessage, error) {
	if r == nil {
		return domain.PeerDialogueMessage{}, fmt.Errorf("%w: peer dialogue message repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := requireDatabase(r.db); err != nil {
		return domain.PeerDialogueMessage{}, err
	}
	if err := contextErr(ctx); err != nil {
		return domain.PeerDialogueMessage{}, err
	}
	if id.Empty() {
		return domain.PeerDialogueMessage{}, fmt.Errorf("%w: peer dialogue message id is required", domain.ErrInvalidArgument)
	}
	return scanPeerDialogueMessage(r.db.QueryRowContext(ctx,
		`SELECT `+peerDialogueMessageColumns+` FROM peer_dialogue_messages AS m WHERE m.id = ?`, string(id)))
}

// GetForParticipant is the single-message counterpart to the scoped dialogue
// read. It is useful for activity details without leaking another pair's
// content.
func (r *PeerDialogueMessageRepository) GetForParticipant(ctx context.Context, dialogueID, participantAgentID, messageID domain.ID) (domain.PeerDialogueMessage, error) {
	if dialogueID.Empty() || participantAgentID.Empty() || messageID.Empty() {
		return domain.PeerDialogueMessage{}, fmt.Errorf("%w: dialogue, participant and message ids are required", domain.ErrInvalidArgument)
	}
	if r == nil || r.db == nil {
		return domain.PeerDialogueMessage{}, fmt.Errorf("%w: peer dialogue message repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return domain.PeerDialogueMessage{}, err
	}
	// The participation proof is the join, not a preceding round-trip. All
	// three ways this can fail — no such message, right message but wrong
	// dialogue, right dialogue but the caller is not one of its two named
	// participants — still collapse to the same ErrNotFound, so a
	// non-participant cannot tell them apart.
	return scanPeerDialogueMessage(r.db.QueryRowContext(ctx,
		`SELECT `+peerDialogueMessageColumns+`
		FROM peer_dialogue_messages AS m
		JOIN peer_dialogues AS d ON d.id = m.dialogue_id
		WHERE m.id = ? AND m.dialogue_id = ?
		  AND (d.initiator_agent_id = ? OR d.peer_agent_id = ?)`,
		string(messageID), string(dialogueID), string(participantAgentID), string(participantAgentID)))
}

// ListByDialogue returns chronological messages only when the caller is a
// participant. Signature order follows the existing ListByConversation API:
// dialogue ID first, participant scope second.
func (r *PeerDialogueMessageRepository) ListByDialogue(ctx context.Context, dialogueID, participantAgentID domain.ID, limit ...int) ([]domain.PeerDialogueMessage, error) {
	if dialogueID.Empty() || participantAgentID.Empty() {
		return nil, fmt.Errorf("%w: dialogue and participant ids are required", domain.ErrInvalidArgument)
	}
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: peer dialogue message repository is unavailable", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	// The scope check is driven from peer_dialogues and the messages are
	// LEFT JOINed onto it, so one statement both proves participation and
	// reads the turns. Driving from the dialogue side is what keeps the two
	// outcomes distinguishable: a caller who is not a participant matches no
	// row at all and gets ErrNotFound, while a participant whose dialogue has
	// somehow lost its messages matches exactly one row whose message columns
	// are NULL and gets an empty slice — the damaged-state signal the bridge
	// reports separately.
	query := `
		SELECT d.id, ` + peerDialogueMessageColumns + `
		FROM peer_dialogues AS d
		LEFT JOIN peer_dialogue_messages AS m ON m.dialogue_id = d.id
		WHERE d.id = ? AND (d.initiator_agent_id = ? OR d.peer_agent_id = ?)
		ORDER BY m.sequence ASC, m.id ASC`
	args := []any{string(dialogueID), string(participantAgentID), string(participantAgentID)}
	if len(limit) > 0 && limit[0] > 0 {
		query += " LIMIT ?"
		args = append(args, limit[0])
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrappedSQLError("list peer dialogue messages", err)
	}
	defer rows.Close()
	result := make([]domain.PeerDialogueMessage, 0)
	scoped := false
	for rows.Next() {
		var scopeID string
		var row peerDialogueMessageRow
		if err := rows.Scan(append([]any{&scopeID}, row.dest()...)...); err != nil {
			return nil, wrappedSQLError("scan peer dialogue message", err)
		}
		scoped = true
		if !row.present() {
			continue
		}
		message, buildErr := row.toMessage()
		if buildErr != nil {
			return nil, buildErr
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate peer dialogue messages", err)
	}
	if !scoped {
		return nil, wrappedSQLError("scope peer dialogue messages", sql.ErrNoRows)
	}
	return result, nil
}

// ListByDialogueForParticipant is a participant-first alias for integrations
// that use that argument order elsewhere in their storage code.
func (r *PeerDialogueMessageRepository) ListByDialogueForParticipant(ctx context.Context, participantAgentID, dialogueID domain.ID, limit ...int) ([]domain.PeerDialogueMessage, error) {
	return r.ListByDialogue(ctx, dialogueID, participantAgentID, limit...)
}

type peerDialogueMessageScanner interface {
	Scan(dest ...any) error
}

// peerDialogueMessageColumns is the one column list every message read uses, so
// the single-row reads and the joined list read share peerDialogueMessageRow.
// It is qualified with the "m" alias because both reads join peer_dialogues,
// which carries columns of the same names.
const peerDialogueMessageColumns = `m.id, m.dialogue_id, m.sequence, m.sender_agent_id,
	       m.recipient_agent_id, m.source_run_id, m.content, m.created_at`

// peerDialogueMessageRow is one raw message row. Its fields are nullable
// because the scoped list read LEFT JOINs messages onto the dialogue that
// authorizes them: a participant whose dialogue has no messages produces one
// row with every message column NULL, and that has to be recognized rather
// than fail the scan.
type peerDialogueMessageRow struct {
	id, dialogueID, senderID, recipientID, sourceRunID, content, createdAt sql.NullString
	sequence                                                               sql.NullInt64
}

func (row *peerDialogueMessageRow) dest() []any {
	return []any{&row.id, &row.dialogueID, &row.sequence, &row.senderID,
		&row.recipientID, &row.sourceRunID, &row.content, &row.createdAt}
}

// present reports whether the row carries a message at all, as opposed to the
// all-NULL placeholder an unmatched LEFT JOIN produces.
func (row *peerDialogueMessageRow) present() bool { return row.id.Valid }

func (row *peerDialogueMessageRow) toMessage() (domain.PeerDialogueMessage, error) {
	message := domain.PeerDialogueMessage{
		ID:               domain.ID(row.id.String),
		DialogueID:       domain.ID(row.dialogueID.String),
		Sequence:         int(row.sequence.Int64),
		SenderAgentID:    domain.ID(row.senderID.String),
		RecipientAgentID: domain.ID(row.recipientID.String),
		SourceRunID:      domain.ID(row.sourceRunID.String),
		Content:          row.content.String,
	}
	var err error
	if message.CreatedAt, err = scanTime(row.createdAt.String); err != nil {
		return domain.PeerDialogueMessage{}, err
	}
	if err := message.Validate(); err != nil {
		return domain.PeerDialogueMessage{}, err
	}
	return message, nil
}

func scanPeerDialogueMessage(scanner peerDialogueMessageScanner) (domain.PeerDialogueMessage, error) {
	var row peerDialogueMessageRow
	if err := scanner.Scan(row.dest()...); err != nil {
		return domain.PeerDialogueMessage{}, wrappedSQLError("scan peer dialogue message", err)
	}
	return row.toMessage()
}

var _ interface {
	Get(context.Context, domain.ID) (domain.PeerDialogue, error)
	GetForParticipant(context.Context, domain.ID, domain.ID) (domain.PeerDialogue, error)
	FindByIdempotencyKey(context.Context, domain.ID, domain.ID, string) (domain.PeerDialogue, error)
	HasByTriggerRun(context.Context, domain.ID, domain.ID) (bool, error)
	HasRecentPair(context.Context, string, time.Time) (bool, error)
	RecoverInterrupted(context.Context, time.Time, string) error
	Save(context.Context, domain.PeerDialogue) error
} = (*PeerDialogueRepository)(nil)

var _ interface {
	Get(context.Context, domain.ID) (domain.PeerDialogueMessage, error)
	ListByDialogue(context.Context, domain.ID, domain.ID, ...int) ([]domain.PeerDialogueMessage, error)
} = (*PeerDialogueMessageRepository)(nil)
