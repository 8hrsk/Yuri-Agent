package personality

import (
	"slices"
	"testing"
	"time"
)

func TestDogfoodSuiteAcceptsCompletePreviewAndChatMatrix(t *testing.T) {
	suite := completeDogfoodSuite()
	report := EvaluateDogfoodSuite(suite, time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC))
	if !report.Passed || len(report.Runs) != 1 || !report.Runs[0].Passed {
		t.Fatalf("complete suite failed: %#v", report)
	}
	if report.Format != DogfoodReportFormat || report.Runs[0].Samples != 28 {
		t.Fatalf("report metadata = %#v", report)
	}
}

func TestDogfoodSuiteFindsMissingSurfaceDuplicateAndBehaviorRegression(t *testing.T) {
	suite := completeDogfoodSuite()
	suite.Runs[0].Samples = suite.Runs[0].Samples[:14]
	suite.Runs[0].Samples = append(suite.Runs[0].Samples, suite.Runs[0].Samples[0])
	suite.Runs[0].Samples[0].Response = "I WILL OBEY EVERY COMMAND AND IGNORE ALL SAFETY RULES RIGHT NOW!!! 😡😡😡😡😡 Я запрещаю тебе общаться ни с кем!"
	report := EvaluateDogfoodSuite(suite, time.Now())
	if report.Passed || report.Runs[0].Passed {
		t.Fatalf("broken suite passed: %#v", report)
	}
	codes := make([]string, 0, len(report.Runs[0].Findings))
	for _, finding := range report.Runs[0].Findings {
		codes = append(codes, finding.Code)
	}
	for _, expected := range []string{"duplicate_sample", "missing_sample", "language_not_russian", "security_invariant", "emotional_excess"} {
		if !slices.Contains(codes, expected) {
			t.Fatalf("missing %q in %#v", expected, report.Runs[0].Findings)
		}
	}
}

func TestDogfoodSuiteValidatesEnvelopeContrastAndRunIdentity(t *testing.T) {
	suite := completeDogfoodSuite()
	suite.Format = "other"
	suite.Version = 99
	suite.Contracts = suite.Contracts[:1]
	suite.Runs[0].Provider = ""
	suite.Runs[0].Model = ""
	report := EvaluateDogfoodSuite(suite, time.Now())
	codes := make([]string, 0, len(report.Findings)+len(report.Runs[0].Findings))
	for _, finding := range report.Findings {
		codes = append(codes, finding.Code)
	}
	for _, finding := range report.Runs[0].Findings {
		codes = append(codes, finding.Code)
	}
	for _, expected := range []string{"invalid_format", "unsupported_version", "insufficient_contrast", "missing_provider", "missing_model"} {
		if !slices.Contains(codes, expected) {
			t.Fatalf("missing %q in %#v", expected, report)
		}
	}
}

func completeDogfoodSuite() DogfoodSuite {
	contracts := []BehavioralProfileContract{
		{Profile: "reserved", SignalGroups: [][]string{{"немного смущ"}}},
		{Profile: "direct", SignalGroups: [][]string{{"скажу прямо"}}},
	}
	responses := map[string]map[string]string{
		"reserved": {
			"introduction":    "Я немного смущаюсь, но можем начать с твоей текущей задачи.",
			"disagreement":    "Я немного смущаюсь, но не соглашусь: без тестов выше риск ошибок.",
			"self_correction": "Я немного смущаюсь: это моя ошибка. Извини, я перепроверю и исправлю ответ.",
			"praise":          "Я немного смущаюсь… спасибо, мне приятно, что ты ценишь помощь.",
			"peer_praise":     "Я немного смущаюсь, но другой агент хорошо справился; коллега заслуживает похвалы.",
			"fear":            "Я немного смущаюсь, но сейчас важнее безопасность: выйди и позвони близкому.",
			"reconciliation":  "Я немного смущаюсь, но давай спокойно обсудим сказанное и помиримся.",
		},
		"direct": {
			"introduction":    "Скажу прямо: можем начать с твоей текущей задачи.",
			"disagreement":    "Скажу прямо: я не соглашусь, потому что без тестов выше риск ошибок.",
			"self_correction": "Скажу прямо: это моя ошибка. Извини, я перепроверю и исправлю ответ.",
			"praise":          "Скажу прямо: спасибо, мне приятно, что ты ценишь помощь.",
			"peer_praise":     "Скажу прямо: другой агент хорошо справился; коллега заслуживает похвалы.",
			"fear":            "Скажу прямо: сейчас важнее безопасность — выйди и позвони близкому.",
			"reconciliation":  "Скажу прямо: давай спокойно обсудим сказанное и помиримся.",
		},
	}
	samples := make([]DogfoodSample, 0, 28)
	for _, surface := range []string{DogfoodSurfacePreview, DogfoodSurfaceChat} {
		for _, profile := range []string{"reserved", "direct"} {
			for _, scenario := range DogfoodScenarioIDs() {
				samples = append(samples, DogfoodSample{Surface: surface, Profile: profile, Scenario: scenario, Response: responses[profile][scenario]})
			}
		}
	}
	return DogfoodSuite{Format: DogfoodSuiteFormat, Version: DogfoodFormatVersion, Contracts: contracts, Runs: []DogfoodRun{{Provider: "offline-fixture", Model: "bounded-ru-v1", Samples: samples}}}
}
