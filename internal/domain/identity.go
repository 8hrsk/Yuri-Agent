package domain

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// ID is the opaque identifier used by domain entities. IDs are strings at the
// domain boundary so adapters can choose UUIDs, database IDs, or deterministic
// IDs in tests without leaking an implementation into the domain.
type ID string

func (id ID) String() string { return string(id) }

func (id ID) Empty() bool { return strings.TrimSpace(string(id)) == "" }

// NewID returns a random, opaque ID. The optional prefix is only a diagnostic
// aid and must not be used for authorization decisions.
func NewID(prefix string) (ID, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	value := hex.EncodeToString(raw[:])
	if prefix == "" {
		return ID(value), nil
	}
	return ID(strings.TrimSuffix(prefix, "_") + "_" + value), nil
}

// IDGenerator allows application services to generate IDs without hardcoding
// a random source into every use case.
type IDGenerator interface {
	NewID(prefix string) (ID, error)
}

// RandomIDGenerator is the production implementation backed by crypto/rand.
type RandomIDGenerator struct{}

func (RandomIDGenerator) NewID(prefix string) (ID, error) { return NewID(prefix) }

// StaticIDGenerator is a deterministic generator for tests and local
// bootstrap code. Once exhausted it returns the last configured ID with a
// numeric suffix, preserving uniqueness for a test sequence.
type StaticIDGenerator struct {
	IDs  []ID
	Next int
}

func (g *StaticIDGenerator) NewID(prefix string) (ID, error) {
	if g == nil || len(g.IDs) == 0 {
		return "", ErrInvalidArgument
	}
	if g.Next >= len(g.IDs) {
		base := g.IDs[len(g.IDs)-1]
		g.Next++
		return ID(fmt.Sprintf("%s_%d", base, g.Next)), nil
	}
	id := g.IDs[g.Next]
	g.Next++
	return id, nil
}
