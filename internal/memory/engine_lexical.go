package memory

import (
	"math"
	"math/bits"
	"strings"
	"unicode"
	"unicode/utf8"
)

// isTokenRune is the single definition of what tokenize treats as part of a
// token. Everything else in this file depends on it agreeing with tokenize.
func isTokenRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsDigit(value) || value == '_'
}

// lexicalQuery is the per-query half of LexicalScore, prepared once instead of
// once per stored record.
//
// Recall scores every eligible memory against the same query. The original
// LexicalScore re-tokenized the query, re-tokenized the record's immutable
// content and rebuilt a map of that content's tokens on every single call —
// 44 allocations and 1712 B per record, on a path that walks the whole store.
// Hoisting the query out and streaming the content's token spans produces a
// bit-identical score with at most one allocation per record: the lowercased
// content that the phrase check needs anyway.
//
// The equivalence is not an optimisation detail. Recall's ordering is a
// function of this score, so TestLexicalScoreEquivalence holds this type to a
// verbatim copy of the original algorithm over an adversarial corpus.
type lexicalQuery struct {
	// tokens is tokenize(query); its length is the coverage denominator, so
	// repeated query tokens must be preserved rather than deduplicated.
	tokens []string
	// positions maps a token to the bitmask of the query positions it fills,
	// which is what makes multiplicity survive the streaming scan.
	positions map[string]uint64
	// counts is the >64-token fallback for positions.
	counts map[string]int
	lower  string
	all    uint64
}

// maxMaskedQueryTokens is the width of the bitmask fast path. A recall query
// with more tokens than this is not realistic, but it must still be scored
// correctly, so the slow path exists.
const maxMaskedQueryTokens = 64

func newLexicalQuery(query string) lexicalQuery {
	trimmed := strings.TrimSpace(query)
	prepared := lexicalQuery{tokens: tokenize(trimmed), lower: strings.ToLower(trimmed)}
	if len(prepared.tokens) == 0 {
		return prepared
	}
	if len(prepared.tokens) <= maxMaskedQueryTokens {
		prepared.positions = make(map[string]uint64, len(prepared.tokens))
		for index, token := range prepared.tokens {
			prepared.positions[token] |= uint64(1) << uint(index)
			prepared.all |= uint64(1) << uint(index)
		}
		return prepared
	}
	prepared.counts = make(map[string]int, len(prepared.tokens))
	for _, token := range prepared.tokens {
		prepared.counts[token]++
	}
	return prepared
}

// score reproduces LexicalScore(content, query) exactly for the query this
// value was built from.
func (q lexicalQuery) score(content string) float64 {
	if len(q.tokens) == 0 {
		return 0
	}
	// Lowercasing the whole content once serves both halves of the score.
	// Token boundaries are unaffected: unicode.ToLower maps letters to
	// letters and leaves digits, '_' and separators alone, so the token spans
	// of the lowered string are exactly tokenize(content).
	lower := strings.ToLower(content)
	matched, hasToken := q.matchContent(lower)
	if !hasToken {
		// The original returns 0 before the phrase check when the content has
		// no tokens at all. Keep that, even though a phrase could still match.
		return 0
	}
	coverage := float64(matched) / float64(len(q.tokens))
	phrase := 0.0
	if strings.Contains(lower, q.lower) {
		phrase = 0.25
	}
	return clamp01(coverage*0.75 + phrase)
}

// matchContent walks the lowered content once and reports how many query
// positions its token set covers, plus whether the content had any token.
func (q lexicalQuery) matchContent(lower string) (matched int, hasToken bool) {
	if q.positions == nil {
		return q.matchContentCounted(lower)
	}
	var found uint64
	start := -1
	for index, value := range lower {
		if isTokenRune(value) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 {
			hasToken = true
			found |= q.positions[lower[start:index]]
			start = -1
			if found == q.all {
				return bits.OnesCount64(found), true
			}
		}
	}
	if start >= 0 {
		hasToken = true
		found |= q.positions[lower[start:]]
	}
	return bits.OnesCount64(found), hasToken
}

// matchContentCounted handles queries wider than the bitmask. It is written
// for correctness, not speed; no realistic recall query reaches it.
func (q lexicalQuery) matchContentCounted(lower string) (matched int, hasToken bool) {
	seen := make(map[string]struct{}, len(q.counts))
	add := func(token string) {
		if _, ok := seen[token]; ok {
			return
		}
		seen[token] = struct{}{}
		matched += q.counts[token]
	}
	start := -1
	for index, value := range lower {
		if isTokenRune(value) {
			if start < 0 {
				start = index
			}
			continue
		}
		if start >= 0 {
			hasToken = true
			add(lower[start:index])
			start = -1
		}
	}
	if start >= 0 {
		hasToken = true
		add(lower[start:])
	}
	return matched, hasToken
}

// canonicalTextEquals reports whether canonicalText(content) == canonical
// without materializing the content's tokens.
//
// The deduplication scan in resolveExisting runs this once per stored record
// for every written candidate. Building each record's canonical form only to
// discard it made that scan O(N x content length) in tokenizations and
// allocations; streaming it stops at the first rune that cannot match, which
// for almost every record is the first rune of the first token.
func canonicalTextEquals(content, canonical string) bool {
	position := 0
	inToken := false
	started := false
	for _, value := range content {
		if !isTokenRune(value) {
			inToken = false
			continue
		}
		if !inToken {
			if started {
				// canonicalText joins tokens with a single space.
				if position >= len(canonical) || canonical[position] != ' ' {
					return false
				}
				position++
			}
			inToken = true
			started = true
		}
		// tokenize lowercases each token with strings.ToLower, which is a
		// per-rune unicode.ToLower map, so comparing rune by rune is exact.
		expected, size := utf8.DecodeRuneInString(canonical[position:])
		if size == 0 || expected != unicode.ToLower(value) {
			return false
		}
		position += size
	}
	return position == len(canonical)
}

// affectiveQuery reports whether a query carries an affective intent. It is
// the query-only half of affectiveRelevance, so Recall can decide it once
// instead of re-tokenizing the query for every stored record.
func affectiveQuery(query string) bool {
	for _, token := range tokenize(query) {
		for _, stem := range affectiveQueryStems {
			if strings.HasPrefix(token, stem) {
				return true
			}
		}
	}
	return false
}

// affectiveRelevanceFor is affectiveRelevance with the query decision already
// made. Both of the original's branches yield the same value, so the record
// is affective when either its own nature/kind or the query says so.
func affectiveRelevanceFor(affective bool, memory Memory) float64 {
	if affective || memory.Nature == ContentEmotion || memory.Kind == KindRelationship {
		return math.Abs(memory.Valence)
	}
	return 0
}
