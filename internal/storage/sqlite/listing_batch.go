package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

// The reads in this file exist to collapse the N+1 the desktop bridge used to
// issue while assembling a conversation list: one messages query and one runs
// query per conversation, then one tool-call query per run. They answer for a
// whole page of owners in a single statement, using SQLite's ROW_NUMBER window
// function to keep the per-owner limit inside the query rather than fetching
// everything and trimming in Go.

// eachChunkRow runs one chunk's statement and feeds every row to scan.
//
// It exists so `defer rows.Close()` can be written at all. The five set-based
// reads below all iterate chunks of ids, and a `defer` inside that loop would
// hold every chunk's rows open until the whole read returned — which is why the
// loops used to close by hand on each of their three exits, and why
// sqlclosecheck flagged each one. Pulling one chunk into its own function makes
// the deferred close correct and collapses the three hand-written exits into
// one.
//
// The close error is still reported. It is only allowed to overwrite a nil
// error, so a scan or iteration failure is never masked by it.
func eachChunkRow(ctx context.Context, db *sql.DB, subject, query string, args []any, scan func(rowScanner) error) (err error) {
	rows, queryErr := db.QueryContext(ctx, query, args...)
	if queryErr != nil {
		return wrappedSQLError("list "+subject, queryErr)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = wrappedSQLError("close "+subject, closeErr)
		}
	}()
	for rows.Next() {
		if scanErr := scan(rows); scanErr != nil {
			return scanErr
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return wrappedSQLError("iterate "+subject, rowsErr)
	}
	return nil
}

// ErrCursorNotFound reports that a paging cursor names a message that is not
// in the conversation being paged: a stale id, an id belonging to another
// conversation, or one that was never persisted at all.
//
// It exists because the alternative is silence. "The cursor is unknown" and
// "the transcript starts here" both produce an empty page, so without a
// separate signal a caller that hands back a bad id cannot tell a bug from the
// end of the list — it simply stops paging and shows nothing. It is returned
// as an error rather than as a flag on the result because an error is the one
// thing a Go caller cannot forget to look at: every caller of ListBefore
// already has to branch on err, whereas a third state on the returned slice
// would have to be checked by each of them, and the one that forgot would be
// back to the silent empty page this replaces.
//
// It also satisfies errors.Is(err, domain.ErrInvalidArgument): an unknown
// cursor is a caller bug of the same family as a negative limit, so a boundary
// that already maps invalid arguments to a rejection keeps doing so.
var ErrCursorNotFound = errors.New("paging cursor is not a message of this conversation")

// perOwnerTail renders a query that keeps only the newest limit rows of each
// owner. columns must be the bare column list of the table, ownerColumn the
// partition key, and orderColumns the undirected column list that orders the
// table oldest-first — the window function ranks by its descending form so the
// per-owner cut keeps the newest rows, and the outer query then hands them
// back oldest-first.
func perOwnerTail(columns, table, ownerColumn, orderColumns, placeholders string) string {
	return `SELECT ` + columns + `
		FROM (
			SELECT ` + columns + `,
			       ROW_NUMBER() OVER (PARTITION BY ` + ownerColumn + ` ORDER BY ` + reverseOrder(orderColumns) + `) AS tail_rank
			FROM ` + table + `
			WHERE ` + ownerColumn + ` IN (` + placeholders + `)
		)
		WHERE tail_rank <= ?
		ORDER BY ` + orderColumns
}

// reverseOrder turns an undirected ORDER BY column list into its descending
// form so the window function ranks the newest row first.
func reverseOrder(order string) string {
	parts := strings.Split(order, ",")
	for index, part := range parts {
		parts[index] = strings.TrimSpace(part) + " DESC"
	}
	return strings.Join(parts, ", ")
}

// ListTailByConversations returns the newest perConversation messages of every
// listed conversation, each conversation's slice in chronological order. It is
// the set-based replacement for calling ListByConversation in a loop: one
// statement answers for the whole page (chunked only if the page exceeds
// idChunkSize ids).
//
// A conversation with no messages is absent from the map rather than present
// with an empty slice, so a caller can tell "not asked for" from "empty".
func (r *MessageRepository) ListTailByConversations(ctx context.Context, conversationIDs []domain.ID, perConversation int) (map[domain.ID][]Message, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit, err := boundedPerOwnerLimit("message", perConversation)
	if err != nil {
		return nil, err
	}
	result := make(map[domain.ID][]Message)
	for _, chunk := range chunkIDs(conversationIDs) {
		placeholders, args := idPlaceholders(chunk)
		query := perOwnerTail(messageColumns, "messages", "conversation_id", "created_at, id", placeholders)
		args = append(args, limit)
		if err := eachChunkRow(ctx, r.db, "messages by conversations", query, args, func(row rowScanner) error {
			message, scanErr := scanMessage(row)
			if scanErr != nil {
				return scanErr
			}
			result[message.ConversationID] = append(result[message.ConversationID], message)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// conversationPreviewRunes is how much of a conversation's newest message the
// sidebar shows. It is applied inside the query so a metadata list never reads
// a whole message body: the saving is the point of reading previews separately
// from transcripts at all.
const conversationPreviewRunes = 100

// ListPreviewsByConversations returns the leading characters of the newest
// non-empty message of every listed conversation, in one statement.
//
// It exists so a conversation list can carry the snippet its sidebar renders
// without carrying the transcripts it does not. The alternative the desktop
// bridge used was to read a tail of messages per conversation purely so the
// last of them could be truncated to a preview, which read — and shipped to the
// renderer — three orders of magnitude more text than the sidebar draws.
//
// Empty messages are skipped rather than counted, matching the rule the bridge
// applied while walking a tail: a tool message with no content is not what the
// reader is looking for when they scan the sidebar for a conversation.
func (r *MessageRepository) ListPreviewsByConversations(ctx context.Context, conversationIDs []domain.ID) (map[domain.ID]string, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID]string)
	for _, chunk := range chunkIDs(conversationIDs) {
		placeholders, args := idPlaceholders(chunk)
		query := `SELECT conversation_id, preview
			FROM (
				SELECT conversation_id,
				       substr(content, 1, ?) AS preview,
				       ROW_NUMBER() OVER (PARTITION BY conversation_id ORDER BY created_at DESC, id DESC) AS tail_rank
				FROM messages
				WHERE conversation_id IN (` + placeholders + `) AND content <> ''
			)
			WHERE tail_rank = 1`
		if err := eachChunkRow(ctx, r.db, "conversation previews", query, append([]any{conversationPreviewRunes}, args...), func(row rowScanner) error {
			var id, preview string
			if scanErr := row.Scan(&id, &preview); scanErr != nil {
				return wrappedSQLError("scan conversation preview", scanErr)
			}
			result[domain.ID(id)] = preview
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListBefore returns the page of messages immediately older than beforeID, in
// chronological order — the read behind the transcript's "show earlier"
// control.
//
// The cursor is a message id rather than a timestamp because the transcript is
// ordered by (created_at, id): two messages can share a timestamp, and paging
// on the timestamp alone would either skip or repeat one of them at a page
// boundary. The comparison is strictly less than the anchor row, so the anchor
// itself is never returned twice and nothing between the pages is lost.
//
// An empty beforeID means "the newest page". A beforeID that is not a message
// of this conversation is ErrCursorNotFound, never an empty page: the two used
// to be the same answer, so a caller paging with a stale or never-persisted id
// stopped silently instead of reporting the bug.
//
// The anchor is resolved inside the conversation, not across the whole table.
// beforeID reaches this method straight from the renderer, and an id belonging
// to a different conversation used to resolve anyway — the page then came back
// scoped to the right conversation but cut at a foreign message's timestamp,
// which is a wrong page returned as if it were right.
func (r *MessageRepository) ListBefore(ctx context.Context, conversationID domain.ID, beforeID domain.ID, limit int) ([]Message, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if conversationID.Empty() {
		return nil, fmt.Errorf("%w: conversation id is required", domain.ErrInvalidArgument)
	}
	bounded, err := boundedPerOwnerLimit("message", limit)
	if err != nil {
		return nil, err
	}
	if beforeID.Empty() {
		return r.ListByConversation(ctx, conversationID, bounded)
	}
	// The anchor subquery is scoped to the conversation and bounded. The bound
	// is belt and braces — id is the primary key of messages, so the lookup
	// already yields at most one row — but it states the intent the surrounding
	// cross join depends on.
	query := `SELECT ` + messageColumns + `
		FROM (
			SELECT ` + messageColumns + `
			FROM messages, (
				SELECT created_at AS anchor_created_at, id AS anchor_id
				FROM messages WHERE id = ? AND conversation_id = ? LIMIT 1
			)
			WHERE conversation_id = ?
			  AND (created_at < anchor_created_at OR (created_at = anchor_created_at AND id < anchor_id))
			ORDER BY created_at DESC, id DESC
			LIMIT ?
		)
		ORDER BY created_at ASC, id ASC`
	rows, err := r.db.QueryContext(ctx, query, string(beforeID), string(conversationID), string(conversationID), bounded)
	if err != nil {
		return nil, wrappedSQLError("list messages before cursor", err)
	}
	defer rows.Close()
	result := make([]Message, 0)
	for rows.Next() {
		message, scanErr := scanMessage(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, message)
	}
	if err := rows.Err(); err != nil {
		return nil, wrappedSQLError("iterate messages before cursor", err)
	}
	if len(result) > 0 {
		// A row older than the anchor is proof the anchor resolved, so the
		// common page still costs the single round-trip it always did. Only the
		// terminal page pays for the check below, and it is a primary-key point
		// lookup.
		return result, nil
	}
	known, err := r.cursorExists(ctx, conversationID, beforeID)
	if err != nil {
		return nil, err
	}
	if !known {
		return nil, fmt.Errorf("%w: %w %q", domain.ErrInvalidArgument, ErrCursorNotFound, string(beforeID))
	}
	return result, nil
}

// cursorExists reports whether beforeID names a message of this conversation.
// It is only reached for an empty page, where it is the difference between
// "the transcript starts here" and "that cursor is not real".
func (r *MessageRepository) cursorExists(ctx context.Context, conversationID, beforeID domain.ID) (bool, error) {
	var found int
	err := r.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM messages WHERE id = ? AND conversation_id = ?)`,
		string(beforeID), string(conversationID),
	).Scan(&found)
	if err != nil {
		return false, wrappedSQLError("resolve message cursor", err)
	}
	return found == 1, nil
}

// ListRecentByConversations returns the newest perConversation runs of every
// listed conversation, each slice in creation order. It replaces the
// per-conversation ListByConversation call the bridge made in a loop.
func (r *RunRepository) ListRecentByConversations(ctx context.Context, conversationIDs []domain.ID, perConversation int) (map[domain.ID][]domain.AgentRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	limit, err := boundedPerOwnerLimit("run", perConversation)
	if err != nil {
		return nil, err
	}
	result := make(map[domain.ID][]domain.AgentRun)
	for _, chunk := range chunkIDs(conversationIDs) {
		placeholders, args := idPlaceholders(chunk)
		query := perOwnerTail(runColumns, "agent_runs", "conversation_id", "created_at, id", placeholders)
		args = append(args, limit)
		if err := eachChunkRow(ctx, r.db, "runs by conversations", query, args, func(row rowScanner) error {
			run, scanErr := scanRun(row)
			if scanErr != nil {
				return scanErr
			}
			result[run.ConversationID] = append(result[run.ConversationID], run)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListByIDs reads the named runs in one statement, keyed by id. Ids that do
// not exist are simply absent from the map.
func (r *RunRepository) ListByIDs(ctx context.Context, ids []domain.ID) (map[domain.ID]domain.AgentRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID]domain.AgentRun)
	for _, chunk := range chunkIDs(ids) {
		placeholders, args := idPlaceholders(chunk)
		query := runSelect + ` WHERE id IN (` + placeholders + `) ORDER BY created_at ASC, id ASC`
		if err := eachChunkRow(ctx, r.db, "runs by ids", query, args, func(row rowScanner) error {
			run, scanErr := scanRun(row)
			if scanErr != nil {
				return scanErr
			}
			result[run.ID] = run
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListChildrenByParents reads anonymous child runs for a bounded set of root
// runs. Stage 8 limits children per parent at creation time; this batch lookup
// keeps conversation history from falling back to one query per delegation.
func (r *RunRepository) ListChildrenByParents(ctx context.Context, parentRunIDs []domain.ID) (map[domain.ID][]domain.AgentRun, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID][]domain.AgentRun)
	for _, chunk := range chunkIDs(parentRunIDs) {
		placeholders, args := idPlaceholders(chunk)
		query := runSelect + ` WHERE parent_run_id IN (` + placeholders + `) ORDER BY created_at ASC, id ASC`
		if err := eachChunkRow(ctx, r.db, "child runs by parents", query, args, func(row rowScanner) error {
			run, scanErr := scanRun(row)
			if scanErr != nil {
				return scanErr
			}
			result[run.ParentRunID] = append(result[run.ParentRunID], run)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListByIDs reads the named agent profiles in one statement, keyed by id. Ids
// that do not exist are simply absent from the map.
//
// It replaces the per-owner Get the peer-dialogue list called twice for every
// dialogue on its page — once for the initiator and once for the peer — to
// print two names. A page of dialogues almost always spans two or three
// distinct agents, so that was up to 100 point lookups for 3 rows.
func (r *AgentRepository) ListByIDs(ctx context.Context, ids []domain.ID) (map[domain.ID]domain.AgentProfile, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID]domain.AgentProfile)
	for _, chunk := range chunkIDs(ids) {
		placeholders, args := idPlaceholders(chunk)
		query := `SELECT id, name, age, gender, preferences, backstory, provider_id, model, created_at, updated_at
			FROM agent_profiles WHERE id IN (` + placeholders + `) ORDER BY created_at, id`
		if err := eachChunkRow(ctx, r.db, "agent profiles by ids", query, args, func(row rowScanner) error {
			profile, scanErr := scanAgentProfile(row)
			if scanErr != nil {
				return scanErr
			}
			result[profile.ID] = profile
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListByIDs reads current relationship heads in bounded set-based queries.
// Missing IDs are absent from the result map.
func (r *RelationshipRepository) ListByIDs(ctx context.Context, ids []domain.ID) (map[domain.ID]domain.RelationshipState, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID]domain.RelationshipState)
	for _, chunk := range chunkIDs(ids) {
		placeholders, args := idPlaceholders(chunk)
		query := relationshipSelect + ` FROM relationship_heads AS rh
			JOIN relationship_versions AS rv ON rv.relationship_id = rh.relationship_id AND rv.version = rh.version
			WHERE rh.relationship_id IN (` + placeholders + `) ORDER BY rh.updated_at DESC, rh.relationship_id`
		if err := eachChunkRow(ctx, r.db, "relationships by ids", query, args, func(row rowScanner) error {
			state, scanErr := scanRelationship(row)
			if scanErr != nil {
				return scanErr
			}
			result[state.ID] = state
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListByDialogues reads the turns of every listed dialogue that the participant
// is actually a party to, in one statement, each dialogue's slice in sequence
// order.
//
// It is the set-based form of ListByDialogue and keeps that read's two
// load-bearing properties exactly:
//
//   - The scope check drives the query. Messages are LEFT JOINed onto
//     peer_dialogues, and the participation predicate is on the dialogue side,
//     so a caller who is not a party to a dialogue matches no row for it at all.
//   - "Not a participant" and "participant, but the dialogue has lost its
//     messages" stay distinguishable. The second is damaged durable state that
//     the bridge reports separately, and an inner join would silently turn it
//     into the first. A scoped dialogue is therefore present in the map with a
//     non-nil empty slice, while an unscoped one is absent — so a caller tells
//     the two apart with the map's comma-ok, not by checking for length zero.
func (r *PeerDialogueMessageRepository) ListByDialogues(ctx context.Context, dialogueIDs []domain.ID, participantAgentID domain.ID) (map[domain.ID][]domain.PeerDialogueMessage, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("%w: peer dialogue message repository is unavailable", domain.ErrInvalidArgument)
	}
	if participantAgentID.Empty() {
		return nil, fmt.Errorf("%w: participant id is required", domain.ErrInvalidArgument)
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID][]domain.PeerDialogueMessage)
	for _, chunk := range chunkIDs(dialogueIDs) {
		placeholders, args := idPlaceholders(chunk)
		query := `
			SELECT d.id, ` + peerDialogueMessageColumns + `
			FROM peer_dialogues AS d
			LEFT JOIN peer_dialogue_messages AS m ON m.dialogue_id = d.id
			WHERE d.id IN (` + placeholders + `) AND (d.initiator_agent_id = ? OR d.peer_agent_id = ?)
			ORDER BY d.id ASC, m.sequence ASC, m.id ASC`
		args = append(args, string(participantAgentID), string(participantAgentID))
		if err := eachChunkRow(ctx, r.db, "peer dialogue messages by dialogues", query, args, func(row rowScanner) error {
			var scopeID string
			var raw peerDialogueMessageRow
			if scanErr := row.Scan(append([]any{&scopeID}, raw.dest()...)...); scanErr != nil {
				return wrappedSQLError("scan peer dialogue message", scanErr)
			}
			scoped := domain.ID(scopeID)
			if _, seen := result[scoped]; !seen {
				// Present-but-empty is the damaged-state signal; it has to be
				// recorded on the placeholder row, which is the only row an
				// empty dialogue produces.
				result[scoped] = []domain.PeerDialogueMessage{}
			}
			if !raw.present() {
				return nil
			}
			message, buildErr := raw.toMessage()
			if buildErr != nil {
				return buildErr
			}
			result[scoped] = append(result[scoped], message)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ListByRuns reads the tool calls of every listed run in one statement, each
// run's slice in creation order. It replaces the per-run ListByRun call the
// bridge made while expanding run traces.
func (r *ToolCallRepository) ListByRuns(ctx context.Context, runIDs []domain.ID) (map[domain.ID][]ToolCall, error) {
	if err := requireDatabase(r.db); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	result := make(map[domain.ID][]ToolCall)
	for _, chunk := range chunkIDs(runIDs) {
		placeholders, args := idPlaceholders(chunk)
		query := `SELECT ` + toolCallColumns + `
			FROM tool_calls WHERE run_id IN (` + placeholders + `) ORDER BY run_id ASC, created_at ASC, id ASC`
		if err := eachChunkRow(ctx, r.db, "tool calls by runs", query, args, func(row rowScanner) error {
			call, scanErr := scanToolCall(row)
			if scanErr != nil {
				return scanErr
			}
			result[call.RunID] = append(result[call.RunID], call)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return result, nil
}
