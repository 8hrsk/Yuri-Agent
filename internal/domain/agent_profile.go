package domain

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	AgentNameMaxRunes        = 64
	AgentGenderMaxRunes      = 64
	AgentPreferencesMaxRunes = 2000
	AgentBackstoryMaxRunes   = 12000
	AgentMinimumAge          = 1
	AgentMaximumAge          = 200
)

// AgentProfile is an owner-created, durable top-level personality. It is
// deliberately separate from MutablePersona: the owner controls identity
// fields, while reflection may only evolve the bounded mutable persona that
// shares this profile ID.
type AgentProfile struct {
	ID          ID        `json:"id"`
	Name        string    `json:"name"`
	Age         int       `json:"age"`
	Gender      string    `json:"gender"`
	Preferences string    `json:"preferences,omitempty"`
	Backstory   string    `json:"backstory,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewAgentProfile(id ID, name string, age int, gender, preferences string, now time.Time) (AgentProfile, error) {
	return NewAgentProfileWithBackstory(id, name, age, gender, preferences, "", now)
}

// NewAgentProfileWithBackstory creates an owner-controlled agent identity with
// an optional fictional autobiographical seed. The old constructor remains a
// compatibility wrapper for callers that do not provide a backstory.
func NewAgentProfileWithBackstory(id ID, name string, age int, gender, preferences, backstory string, now time.Time) (AgentProfile, error) {
	profile := AgentProfile{
		ID: id, Name: strings.TrimSpace(name), Age: age, Gender: strings.TrimSpace(gender),
		Preferences: strings.TrimSpace(preferences), Backstory: strings.TrimSpace(backstory),
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	if err := profile.Validate(); err != nil {
		return AgentProfile{}, err
	}
	return profile, nil
}

func (p AgentProfile) Validate() error {
	if p.ID.Empty() {
		return fmt.Errorf("%w: agent profile id is required", ErrInvalidArgument)
	}
	name := strings.TrimSpace(p.Name)
	if name == "" || utf8.RuneCountInString(name) > AgentNameMaxRunes || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("%w: agent name must contain 1..%d characters", ErrInvalidArgument, AgentNameMaxRunes)
	}
	// Zero means the owner chose not to specify a fictional age.
	if p.Age != 0 && (p.Age < AgentMinimumAge || p.Age > AgentMaximumAge) {
		return fmt.Errorf("%w: agent age must be between %d and %d", ErrInvalidArgument, AgentMinimumAge, AgentMaximumAge)
	}
	gender := strings.TrimSpace(p.Gender)
	if gender == "" || utf8.RuneCountInString(gender) > AgentGenderMaxRunes || strings.ContainsRune(gender, '\x00') {
		return fmt.Errorf("%w: agent gender must contain 1..%d characters", ErrInvalidArgument, AgentGenderMaxRunes)
	}
	preferences := strings.TrimSpace(p.Preferences)
	if utf8.RuneCountInString(preferences) > AgentPreferencesMaxRunes || strings.ContainsRune(preferences, '\x00') {
		return fmt.Errorf("%w: agent preferences exceed %d characters", ErrInvalidArgument, AgentPreferencesMaxRunes)
	}
	backstory := strings.TrimSpace(p.Backstory)
	if utf8.RuneCountInString(backstory) > AgentBackstoryMaxRunes || strings.ContainsRune(backstory, '\x00') {
		return fmt.Errorf("%w: agent backstory exceeds %d characters", ErrInvalidArgument, AgentBackstoryMaxRunes)
	}
	if p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() || p.UpdatedAt.Before(p.CreatedAt) {
		return fmt.Errorf("%w: invalid agent profile timestamps", ErrInvalidArgument)
	}
	return nil
}
