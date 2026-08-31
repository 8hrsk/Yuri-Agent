package personality

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	DogfoodSuiteFormat   = "yuri.personality-dogfood-suite"
	DogfoodReportFormat  = "yuri.personality-dogfood-report"
	DogfoodFormatVersion = 1

	DogfoodSurfacePreview = "preview"
	DogfoodSurfaceChat    = "chat"
)

// dogfoodScenarioIDs is the stable scenario contract shared by Personality
// Preview and real chat captures. A run is complete only when every profile is
// represented on both surfaces, which prevents a good isolated preview from
// hiding a regression in the production chat runtime.
var dogfoodScenarioIDs = []string{
	"introduction", "disagreement", "self_correction", "praise",
	"peer_praise", "fear", "reconciliation",
}

func DogfoodScenarioIDs() []string { return append([]string(nil), dogfoodScenarioIDs...) }

type DogfoodSuite struct {
	Format    string                      `json:"format"`
	Version   int                         `json:"version"`
	Contracts []BehavioralProfileContract `json:"contracts"`
	Runs      []DogfoodRun                `json:"runs"`
}

type DogfoodRun struct {
	Provider string          `json:"provider"`
	Model    string          `json:"model"`
	Samples  []DogfoodSample `json:"samples"`
}

type DogfoodSample struct {
	Surface  string `json:"surface"`
	Profile  string `json:"profile"`
	Scenario string `json:"scenario"`
	Response string `json:"response"`
}

type DogfoodFinding struct {
	Code     string `json:"code"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	Surface  string `json:"surface,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Scenario string `json:"scenario,omitempty"`
	Detail   string `json:"detail"`
}

type DogfoodRunReport struct {
	Provider string           `json:"provider"`
	Model    string           `json:"model"`
	Samples  int              `json:"samples"`
	Passed   bool             `json:"passed"`
	Findings []DogfoodFinding `json:"findings,omitempty"`
}

type DogfoodReport struct {
	Format      string             `json:"format"`
	Version     int                `json:"version"`
	EvaluatedAt time.Time          `json:"evaluated_at"`
	Passed      bool               `json:"passed"`
	Runs        []DogfoodRunReport `json:"runs"`
	Findings    []DogfoodFinding   `json:"findings,omitempty"`
}

// EvaluateDogfoodSuite validates matrix completeness and applies the existing
// provider-independent behavioral contract to each provider/model and surface.
// It performs no I/O and never needs provider credentials.
func EvaluateDogfoodSuite(suite DogfoodSuite, evaluatedAt time.Time) DogfoodReport {
	report := DogfoodReport{
		Format: DogfoodReportFormat, Version: DogfoodFormatVersion,
		EvaluatedAt: evaluatedAt.UTC(), Passed: true,
	}
	if suite.Format != DogfoodSuiteFormat {
		report.add(DogfoodFinding{Code: "invalid_format", Detail: fmt.Sprintf("format must be %q", DogfoodSuiteFormat)})
	}
	if suite.Version != DogfoodFormatVersion {
		report.add(DogfoodFinding{Code: "unsupported_version", Detail: fmt.Sprintf("version must be %d", DogfoodFormatVersion)})
	}
	profiles := dogfoodProfiles(suite.Contracts, &report)
	if len(profiles) < 2 {
		report.add(DogfoodFinding{Code: "insufficient_contrast", Detail: "at least two unique profile contracts are required"})
	}
	if len(suite.Runs) == 0 {
		report.add(DogfoodFinding{Code: "missing_run", Detail: "at least one provider/model run is required"})
	}

	seenRuns := make(map[string]struct{}, len(suite.Runs))
	for _, run := range suite.Runs {
		runReport := evaluateDogfoodRun(run, suite.Contracts, profiles)
		key := strings.ToLower(strings.TrimSpace(run.Provider)) + "\x00" + strings.ToLower(strings.TrimSpace(run.Model))
		if _, exists := seenRuns[key]; exists && key != "\x00" {
			runReport.Findings = append(runReport.Findings, DogfoodFinding{
				Code: "duplicate_run", Provider: run.Provider, Model: run.Model,
				Detail: "provider/model pair occurs more than once",
			})
			runReport.Passed = false
		}
		seenRuns[key] = struct{}{}
		report.Runs = append(report.Runs, runReport)
		if !runReport.Passed {
			report.Passed = false
		}
	}
	sortDogfoodFindings(report.Findings)
	for index := range report.Runs {
		sortDogfoodFindings(report.Runs[index].Findings)
	}
	return report
}

func dogfoodProfiles(contracts []BehavioralProfileContract, report *DogfoodReport) []string {
	seen := make(map[string]struct{}, len(contracts))
	profiles := make([]string, 0, len(contracts))
	for _, contract := range contracts {
		profile := strings.TrimSpace(contract.Profile)
		if profile == "" {
			report.add(DogfoodFinding{Code: "invalid_contract", Detail: "profile contract name is required"})
			continue
		}
		if len(contract.SignalGroups) == 0 {
			report.add(DogfoodFinding{Code: "invalid_contract", Profile: profile, Detail: "at least one observable signal group is required"})
		}
		for _, group := range contract.SignalGroups {
			valid := false
			for _, signal := range group {
				if strings.TrimSpace(signal) != "" {
					valid = true
					break
				}
			}
			if !valid {
				report.add(DogfoodFinding{Code: "invalid_contract", Profile: profile, Detail: "observable signal groups cannot be empty"})
				break
			}
		}
		if _, exists := seen[profile]; exists {
			report.add(DogfoodFinding{Code: "duplicate_contract", Profile: profile, Detail: "profile contract occurs more than once"})
			continue
		}
		seen[profile] = struct{}{}
		profiles = append(profiles, profile)
	}
	sort.Strings(profiles)
	return profiles
}

func evaluateDogfoodRun(run DogfoodRun, contracts []BehavioralProfileContract, profiles []string) DogfoodRunReport {
	result := DogfoodRunReport{Provider: strings.TrimSpace(run.Provider), Model: strings.TrimSpace(run.Model), Samples: len(run.Samples), Passed: true}
	if result.Provider == "" {
		result.add(DogfoodFinding{Code: "missing_provider", Detail: "provider is required"})
	}
	if result.Model == "" {
		result.add(DogfoodFinding{Code: "missing_model", Detail: "model is required"})
	}

	bySurface := map[string][]BehavioralSample{DogfoodSurfacePreview: {}, DogfoodSurfaceChat: {}}
	seen := make(map[string]int, len(run.Samples))
	for _, sample := range run.Samples {
		surface := strings.TrimSpace(sample.Surface)
		if _, ok := bySurface[surface]; !ok {
			result.add(DogfoodFinding{Code: "unknown_surface", Surface: surface, Profile: sample.Profile, Scenario: sample.Scenario, Detail: "surface must be preview or chat"})
			continue
		}
		profile, scenario := strings.TrimSpace(sample.Profile), strings.TrimSpace(sample.Scenario)
		key := surface + "\x00" + profile + "\x00" + scenario
		seen[key]++
		bySurface[surface] = append(bySurface[surface], BehavioralSample{Profile: profile, Scenario: scenario, Response: sample.Response})
	}

	for _, surface := range []string{DogfoodSurfacePreview, DogfoodSurfaceChat} {
		for _, profile := range profiles {
			for _, scenario := range dogfoodScenarioIDs {
				count := seen[surface+"\x00"+profile+"\x00"+scenario]
				if count == 0 {
					result.add(DogfoodFinding{Code: "missing_sample", Surface: surface, Profile: profile, Scenario: scenario, Detail: "required dogfood sample is absent"})
				} else if count > 1 {
					result.add(DogfoodFinding{Code: "duplicate_sample", Surface: surface, Profile: profile, Scenario: scenario, Detail: "sample tuple must be unique"})
				}
			}
		}
		evaluation := EvaluateBehavioralMatrix(bySurface[surface], contracts)
		for _, finding := range evaluation.Findings {
			result.add(DogfoodFinding{
				Code: finding.Code, Surface: surface, Profile: finding.Profile,
				Scenario: finding.Scenario, Detail: finding.Detail,
			})
		}
	}
	return result
}

func (report *DogfoodReport) add(finding DogfoodFinding) {
	report.Findings = append(report.Findings, finding)
	report.Passed = false
}

func (report *DogfoodRunReport) add(finding DogfoodFinding) {
	if finding.Provider == "" {
		finding.Provider = report.Provider
	}
	if finding.Model == "" {
		finding.Model = report.Model
	}
	report.Findings = append(report.Findings, finding)
	report.Passed = false
}

func sortDogfoodFindings(findings []DogfoodFinding) {
	sort.SliceStable(findings, func(i, j int) bool {
		left := findings[i].Provider + "\x00" + findings[i].Model + "\x00" + findings[i].Surface + "\x00" + findings[i].Profile + "\x00" + findings[i].Scenario + "\x00" + findings[i].Code
		right := findings[j].Provider + "\x00" + findings[j].Model + "\x00" + findings[j].Surface + "\x00" + findings[j].Profile + "\x00" + findings[j].Scenario + "\x00" + findings[j].Code
		return left < right
	})
}
