package domain

import (
	"strings"
	"testing"
	"time"
)

func TestNewAgentProfileNormalizesOwnerInput(t *testing.T) {
	now := time.Date(2026, 8, 29, 10, 0, 0, 0, time.FixedZone("local", 3*60*60))
	profile, err := NewAgentProfileWithBackstory("agent_yuri", "  Юри  ", 21, "  женщина  ", "  Любит краткие ответы.  ", "  Я выросла в библиотеке.  ", now)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "Юри" || profile.Gender != "женщина" || profile.Preferences != "Любит краткие ответы." || profile.Backstory != "Я выросла в библиотеке." {
		t.Fatalf("profile was not normalized: %#v", profile)
	}
	if profile.CreatedAt.Location() != time.UTC || !profile.CreatedAt.Equal(now) {
		t.Fatalf("created_at = %v", profile.CreatedAt)
	}
}

func TestNewAgentProfileKeepsLegacyConstructorWithoutBackstory(t *testing.T) {
	now := time.Now().UTC()
	profile, err := NewAgentProfile("agent_yuri", "Юри", 21, "female", "", now)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Backstory != "" {
		t.Fatalf("legacy constructor backstory = %q, want empty", profile.Backstory)
	}
}

func TestAgentProfileRejectsInvalidIdentity(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name    string
		profile AgentProfile
	}{
		{name: "empty id", profile: AgentProfile{Name: "Юри", Age: 21, Gender: "женщина", CreatedAt: now, UpdatedAt: now}},
		{name: "invalid age", profile: AgentProfile{ID: "agent", Name: "Юри", Age: 201, Gender: "женщина", CreatedAt: now, UpdatedAt: now}},
		{name: "empty gender", profile: AgentProfile{ID: "agent", Name: "Юри", Age: 21, CreatedAt: now, UpdatedAt: now}},
		{name: "long preferences", profile: AgentProfile{ID: "agent", Name: "Юри", Age: 21, Gender: "женщина", Preferences: strings.Repeat("я", AgentPreferencesMaxRunes+1), CreatedAt: now, UpdatedAt: now}},
		{name: "long backstory", profile: AgentProfile{ID: "agent", Name: "Юри", Age: 21, Gender: "женщина", Backstory: strings.Repeat("я", AgentBackstoryMaxRunes+1), CreatedAt: now, UpdatedAt: now}},
		{name: "backstory with nul", profile: AgentProfile{ID: "agent", Name: "Юри", Age: 21, Gender: "женщина", Backstory: "история\x00персонажа", CreatedAt: now, UpdatedAt: now}},
		{name: "backwards timestamps", profile: AgentProfile{ID: "agent", Name: "Юри", Age: 21, Gender: "женщина", CreatedAt: now, UpdatedAt: now.Add(-time.Second)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.profile.Validate(); err == nil {
				t.Fatalf("Validate() accepted %#v", test.profile)
			}
		})
	}
}
