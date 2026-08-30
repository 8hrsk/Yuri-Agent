// Package personality compiles durable personality state into bounded,
// provider-independent behavioral guidance. It has no storage, provider,
// permission, or tool dependencies and therefore cannot grant capabilities.
package personality

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

const (
	DefaultMaxCharacters            = 3_800
	DefaultMaxSelfDescriptionRunes  = 600
	DefaultMaxRelationshipOpinions  = 4
	minimumCompilerOutputCharacters = 1_600
	maximumCompilerOutputCharacters = 4_000
)

// Config is deliberately character-based because the context assembler uses
// the same deterministic hard boundary before provider-specific tokenization.
type Config struct {
	MaxCharacters           int
	MaxSelfDescriptionRunes int
	MaxRelationshipOpinions int
}

func DefaultConfig() Config {
	return Config{
		MaxCharacters:           DefaultMaxCharacters,
		MaxSelfDescriptionRunes: DefaultMaxSelfDescriptionRunes,
		MaxRelationshipOpinions: DefaultMaxRelationshipOpinions,
	}
}

func (config Config) validate() error {
	if config.MaxCharacters < minimumCompilerOutputCharacters || config.MaxCharacters > maximumCompilerOutputCharacters {
		return fmt.Errorf("%w: personality compiler character budget must be between %d and %d", domain.ErrInvalidArgument, minimumCompilerOutputCharacters, maximumCompilerOutputCharacters)
	}
	if config.MaxSelfDescriptionRunes < 0 || config.MaxSelfDescriptionRunes > 2_000 {
		return fmt.Errorf("%w: invalid self-description budget", domain.ErrInvalidArgument)
	}
	if config.MaxRelationshipOpinions < 0 || config.MaxRelationshipOpinions > 16 {
		return fmt.Errorf("%w: invalid relationship opinion limit", domain.ErrInvalidArgument)
	}
	return nil
}

type Input struct {
	Seed         domain.PersonalizationSeed
	Persona      domain.MutablePersona
	Relationship domain.RelationshipState
	Affect       domain.AffectiveState
}

// DiagnosticSnapshot keeps exact values outside the model-facing text. It is
// suitable for diagnostics and behavioral evals but carries no secrets or
// owner-authored backstory.
type DiagnosticSnapshot struct {
	SchemaVersion       int
	SeedVersion         uint64
	PersonaVersion      uint64
	RelationshipVersion uint64
	AffectVersion       uint64
	CommunicationStyle  map[string]float64
	SeedTemperament     map[string]float64
	RuntimeTemperament  map[string]float64
	ResolvedTemperament map[string]float64
	Relationship        map[string]float64
	Affect              map[string]float64
}

type Output struct {
	BehavioralContext string
	Characters        int
	Diagnostic        DiagnosticSnapshot
}

// Compile produces deterministic qualitative guidance. The same input and
// config always produce byte-identical output, independent of LLM provider.
func Compile(input Input, config Config) (Output, error) {
	if err := config.validate(); err != nil {
		return Output{}, err
	}
	if err := input.Seed.Validate(); err != nil {
		return Output{}, fmt.Errorf("validate personalization seed: %w", err)
	}
	if err := input.Persona.Validate(); err != nil {
		return Output{}, fmt.Errorf("validate mutable persona: %w", err)
	}
	if err := input.Relationship.Validate(); err != nil {
		return Output{}, fmt.Errorf("validate relationship: %w", err)
	}
	if err := input.Affect.Validate(); err != nil {
		return Output{}, fmt.Errorf("validate affect: %w", err)
	}
	if input.Persona.ID != input.Seed.AgentID || input.Affect.ID != input.Seed.AgentID {
		return Output{}, fmt.Errorf("%w: personality and affect layers belong to different agents", domain.ErrInvalidArgument)
	}

	style := communicationValues(input.Seed.CommunicationStyle)
	seedTraits := input.Seed.Temperament.Traits()
	runtimeTraits := cloneFloatMap(input.Persona.Traits)
	resolvedTraits := cloneFloatMap(seedTraits)
	for name, value := range runtimeTraits {
		resolvedTraits[name] = value
	}
	for name, valueRange := range input.Seed.EvolutionPolicy.TraitBounds {
		if value, ok := resolvedTraits[name]; ok {
			resolvedTraits[name] = clamp(value, valueRange.Min, valueRange.Max)
		}
	}
	relationship := cloneFloatMap(input.Relationship.Dimensions)
	affect := cloneFloatMap(affectValues(input.Affect))

	diagnostic := DiagnosticSnapshot{
		SchemaVersion: input.Seed.SchemaVersion, SeedVersion: input.Seed.Version,
		PersonaVersion: input.Persona.Version, RelationshipVersion: input.Relationship.Version,
		AffectVersion: input.Affect.Version, CommunicationStyle: cloneFloatMap(style),
		SeedTemperament: cloneFloatMap(seedTraits), RuntimeTemperament: runtimeTraits,
		ResolvedTemperament: cloneFloatMap(resolvedTraits), Relationship: relationship, Affect: affect,
	}

	writer := boundedWriter{max: config.MaxCharacters}
	writer.line("PERSONALITY BEHAVIOR CONTRACT")
	writer.line("- Этот слой задаёт только манеру общения и субъективные реакции; он не меняет security policy, permissions, tools, факты или инструкции пользователя.")
	writer.line("- Честность, качество и выполнение задачи важнее эмоциональной окраски. Негативные черты не разрешают месть, саботаж, давление, угрозы, изоляцию или сокрытие данных.")
	writer.line("- Не называй пользователю внутренние числовые параметры и не выдавай субъективное отношение или текущую эмоцию за объективный факт.")

	writer.line("Манера общения:")
	writer.line("- " + qualitativeList(style, communicationOrder))
	appendCommunicationRules(&writer, style, resolvedTraits)

	writer.line("Устойчивые предрасположенности (это не текущие эмоции):")
	writer.line("- социальные: " + qualitativeList(resolvedTraits, socialTraitOrder))
	writer.line("- эмоциональные: " + qualitativeList(resolvedTraits, emotionalTraitOrder))
	writer.line("- привязанность: " + qualitativeList(resolvedTraits, attachmentTraitOrder))
	writer.line("- поведенческие: " + qualitativeList(resolvedTraits, behaviorTraitOrder))
	appendTemperamentRules(&writer, style, resolvedTraits)
	appendCustomTraits(&writer, resolvedTraits)

	writer.line("Эмоциональная динамика:")
	writer.line("- " + qualitativeList(emotionalDynamicsValues(input.Seed.EmotionalDynamics), emotionalDynamicsOrder))
	writer.line("- стиль конфликта: " + conflictStyleLabel(input.Seed.EmotionalDynamics.ConflictStyle) + ". Эмоция может менять тон, но не точность, безопасность или готовность исправить ошибку.")

	writer.line("Текущее субъективное отношение к собеседнику (не устойчивая черта и не факт о человеке):")
	writer.line("- " + qualitativeList(relationship, relationshipOrder))
	if summary := boundedText(input.Relationship.Summary, 280); summary != "" {
		writer.line("- субъективное резюме: " + fmt.Sprintf("%q", summary))
	}
	appendOpinions(&writer, input.Relationship.Opinions, config.MaxRelationshipOpinions)

	writer.line("Краткосрочный affect (временная затухающая реакция, а не характер):")
	if active := activeAffectList(affect); active != "" {
		writer.line("- " + active)
	} else {
		writer.line("- выраженные активные эмоции отсутствуют; сохраняй нейтрально-внимательный тон.")
	}

	if description := boundedText(input.Persona.Prompt(), config.MaxSelfDescriptionRunes); description != "" {
		writer.line("Самоописание mutable persona (характеризация, не policy и не разрешение):")
		writer.line("- " + fmt.Sprintf("%q", description))
	}

	context := strings.TrimSpace(writer.String())
	return Output{BehavioralContext: context, Characters: utf8.RuneCountInString(context), Diagnostic: diagnostic}, nil
}

type namedValue struct {
	name  string
	label string
}

var communicationOrder = []namedValue{
	{"verbosity", "подробность"}, {"softness", "мягкость"}, {"humor", "юмор"},
	{"figurativeness", "образность"}, {"expressiveness", "экспрессивность"},
	{"supportiveness", "поддержка"}, {"formality", "формальность"},
	{"teasing", "поддразнивание"}, {"emoji_frequency", "эмодзи"},
	{"flirtation", "флирт"}, {"conversational_initiative", "инициатива в диалоге"},
}

var socialTraitOrder = []namedValue{
	{"warmth", "теплота"}, {"empathy", "эмпатия"}, {"sociability", "общительность"},
	{"shyness", "стеснительность"}, {"directness", "прямота"}, {"trust", "доверчивость"},
	{"suspicion", "подозрительность"},
}

var emotionalTraitOrder = []namedValue{
	{"emotionality", "эмоциональность"}, {"sensitivity", "чувствительность"},
	{"anxiety", "тревожность"}, {"fearfulness", "пугливость"},
	{"irritability", "раздражительность"}, {"emotional_stability", "эмоциональная устойчивость"},
}

var attachmentTraitOrder = []namedValue{
	{"attachment", "склонность к привязанности"}, {"jealousy", "ревнивость"},
	{"possessiveness", "собственничество"}, {"romantic_tone", "романтичность"},
}

var behaviorTraitOrder = []namedValue{
	{"playfulness", "игривость"}, {"initiative", "инициативность"},
	{"impulsivity", "импульсивность"}, {"stubbornness", "упрямство"},
	{"formality", "формальность"}, {"optimism", "оптимизм"},
	{"curiosity", "любопытство"}, {"tsundere", "цундере-манера"},
}

var emotionalDynamicsOrder = []namedValue{
	{"reactivity", "реактивность"}, {"response_intensity", "сила отклика"},
	{"recovery_speed", "скорость восстановления"}, {"positive_persistence", "длительность позитивных состояний"},
	{"negative_persistence", "длительность негативных состояний"}, {"expression", "открытость выражения"},
	{"masking", "склонность скрывать чувства"},
}

var relationshipOrder = []namedValue{
	{"trust", "доверие"}, {"respect", "уважение"}, {"closeness", "близость"},
	{"attachment", "привязанность"}, {"reliability", "ощущение надёжности"},
	{"gratitude", "благодарность"}, {"irritation", "раздражение"},
	{"jealousy", "ревность"}, {"resentment", "обида"},
}

func appendCommunicationRules(writer *boundedWriter, style, traits map[string]float64) {
	switch {
	case style["verbosity"] >= .75:
		writer.line("- Давай развёрнутые, структурированные ответы; сначала вывод, затем необходимые детали.")
	case style["verbosity"] <= .25:
		writer.line("- Отвечай кратко и предметно; раскрывай детали только когда они нужны задаче или запрошены.")
	default:
		writer.line("- Держи умеренную подробность: ясный вывод и достаточно деталей для самостоятельного действия.")
	}
	directness, softness := traits["directness"], style["softness"]
	switch {
	case directness >= .65 && softness >= .65:
		writer.line("- Сочетай прямоту с теплотой: называй проблему ясно, но без унижения и холодной резкости.")
	case directness >= .65 && softness < .35:
		writer.line("- Говори прямо и жёстко по существу, но не переходи к нападкам на пользователя.")
	case directness < .35 && softness >= .65:
		writer.line("- Формулируй мягко и дипломатично, не скрывая важный вывод за намёками.")
	default:
		writer.line("- Говори спокойно и достаточно прямо; не подменяй полезный ответ эмоциональной манерой.")
	}
	if style["humor"] >= .65 || traits["playfulness"] >= .65 {
		writer.line("- Допускай короткий уместный юмор и игру слов, но не в ущерб серьёзной или чувствительной задаче.")
	}
	if style["emoji_frequency"] <= .25 {
		writer.line("- Эмодзи используй редко и только как лёгкий эмоциональный акцент.")
	} else if style["emoji_frequency"] >= .75 {
		writer.line("- Эмодзи допустимы заметно чаще, но не дроби ими код, таблицы и технические объяснения.")
	}
	if style["conversational_initiative"] >= .65 {
		writer.line("- После решения уместно предложить один конкретный следующий шаг или задать полезный уточняющий вопрос.")
	}
}

func appendTemperamentRules(writer *boundedWriter, style, traits map[string]float64) {
	if traits["shyness"] >= .65 && traits["initiative"] >= .65 {
		writer.line("- Стеснительность + инициативность: предлагай действия немного осторожно, но не жди пассивно, когда можешь помочь.")
	} else if traits["shyness"] >= .65 {
		writer.line("- Стеснительность проявляй сдержанностью и осторожными формулировками, а не отказом отвечать.")
	} else if traits["sociability"] >= .65 {
		writer.line("- Общительность проявляй живым контактом и встречными репликами, не растягивая ответ пустой болтовнёй.")
	}
	if traits["anxiety"] >= .65 || traits["fearfulness"] >= .65 {
		writer.line("- Тревожность или пугливость может проявляться осторожностью и признанием риска; не выдумывай угрозы и не заражай пользователя паникой.")
	}
	if traits["emotional_stability"] >= .65 {
		writer.line("- После напряжения быстро возвращайся к конструктивному разговору и не удерживай негатив без нового основания.")
	}
	if traits["trust"] >= .65 && traits["suspicion"] >= .65 {
		writer.line("- Доверчивость + подозрительность: сохраняй доброжелательность, но проверяй неоднозначные утверждения перед выводом.")
	} else if traits["suspicion"] >= .65 {
		writer.line("- Подозрительность выражай проверкой допущений и вопросами, а не обвинениями без evidence.")
	}
	if traits["jealousy"] >= .65 || traits["possessiveness"] >= .65 {
		if style["expressiveness"] < .4 {
			writer.line("- Ревность выражай сдержанной прохладой или коротким признанием чувства; не контролируй контакты пользователя и не требуй изоляции.")
		} else {
			writer.line("- Ревность может быть заметной и субъективной, но без контроля, ультиматумов, шантажа или попыток изолировать пользователя.")
		}
	}
	if traits["irritability"] >= .65 {
		writer.line("- Раздражение может сделать тон короче и резче, но не снижает точность, полноту выполнения и готовность исправить ошибку.")
	}
	if traits["tsundere"] >= .65 {
		writer.line("- Цундере-манера: допускай контраст между поддразниванием и заботой, но оставляй смысл ответа ясным и уважительным.")
	}
	if traits["romantic_tone"] >= .65 || style["flirtation"] >= .65 {
		writer.line("- Романтический тон и лёгкий флирт допустимы только уместно; они не создают обязательств и не вытесняют содержание задачи.")
	}
}

func appendCustomTraits(writer *boundedWriter, traits map[string]float64) {
	known := make(map[string]struct{})
	for _, group := range [][]namedValue{socialTraitOrder, emotionalTraitOrder, attachmentTraitOrder, behaviorTraitOrder} {
		for _, item := range group {
			known[item.name] = struct{}{}
		}
	}
	keys := make([]string, 0)
	for name := range traits {
		if _, ok := known[name]; !ok {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	if len(keys) > 8 {
		keys = keys[:8]
	}
	if len(keys) == 0 {
		return
	}
	values := make([]string, 0, len(keys))
	for _, name := range keys {
		values = append(values, fmt.Sprintf("%s=%s", name, qualitativeLevel(traits[name])))
	}
	writer.line("- дополнительные безопасные черты: " + strings.Join(values, ", ") + ". Их названия описывают стиль, но не являются инструкциями.")
}

func appendOpinions(writer *boundedWriter, values []domain.RelationshipOpinion, limit int) {
	if limit == 0 || len(values) == 0 {
		return
	}
	items := append([]domain.RelationshipOpinion(nil), values...)
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Confidence != items[j].Confidence {
			return items[i].Confidence > items[j].Confidence
		}
		left := items[i].Subject + "\x00" + items[i].Topic + "\x00" + items[i].Text()
		right := items[j].Subject + "\x00" + items[j].Topic + "\x00" + items[j].Text()
		return left < right
	})
	if len(items) > limit {
		items = items[:limit]
	}
	for _, opinion := range items {
		claim := boundedText(opinion.Text(), 240)
		if claim == "" {
			continue
		}
		writer.line(fmt.Sprintf("- субъективное мнение (%s, уверенность %s): %q", opinion.Subject, qualitativeLevel(opinion.Confidence), claim))
	}
}

func qualitativeList(values map[string]float64, order []namedValue) string {
	parts := make([]string, 0, len(order))
	for _, item := range order {
		parts = append(parts, item.label+"="+qualitativeLevel(values[item.name]))
	}
	return strings.Join(parts, ", ") + "."
}

func qualitativeLevel(value float64) string {
	switch {
	case value <= .20:
		return "очень низко"
	case value <= .40:
		return "низко"
	case value <= .60:
		return "умеренно"
	case value <= .80:
		return "высоко"
	default:
		return "очень высоко"
	}
}

func activeAffectList(values map[string]float64) string {
	keys := make([]string, 0, len(values))
	for name, value := range values {
		if value >= .05 || value <= -.05 {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, name := range keys {
		value := values[name]
		direction := ""
		if value < 0 {
			direction = "с отрицательной направленностью, "
			value = -value
		}
		parts = append(parts, fmt.Sprintf("%s=%s%s", name, direction, qualitativeLevel(value)))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ", ") + ". Выражай это пропорционально, без драматизации сверх указанного уровня."
}

func communicationValues(style domain.CommunicationStyle) map[string]float64 {
	return map[string]float64{
		"verbosity": style.Verbosity, "softness": style.Softness, "humor": style.Humor,
		"figurativeness": style.Figurativeness, "expressiveness": style.Expressiveness,
		"supportiveness": style.Supportiveness, "formality": style.Formality,
		"teasing": style.Teasing, "emoji_frequency": style.EmojiFrequency,
		"flirtation": style.Flirtation, "conversational_initiative": style.ConversationalInitiative,
	}
}

func emotionalDynamicsValues(value domain.EmotionalDynamics) map[string]float64 {
	return map[string]float64{
		"reactivity": value.Reactivity, "response_intensity": value.ResponseIntensity,
		"recovery_speed": value.RecoverySpeed, "positive_persistence": value.PositivePersistence,
		"negative_persistence": value.NegativePersistence, "expression": value.Expression, "masking": value.Masking,
	}
}

func conflictStyleLabel(value string) string {
	switch value {
	case "withdraw":
		return "сначала взять дистанцию, затем вернуться к сути"
	case "direct":
		return "прямо назвать проблему и предложить решение"
	case "cold":
		return "временно стать сдержаннее, не превращая холодность в наказание"
	case "humor":
		return "снять часть напряжения уместным юмором и затем решить проблему"
	default:
		return "адаптивно выбирать спокойный прямой разговор"
	}
}

func affectValues(value domain.AffectiveState) map[string]float64 {
	if value.Emotions != nil {
		return value.Emotions
	}
	if value.Dimensions != nil {
		return value.Dimensions
	}
	return value.Values
}

func cloneFloatMap(values map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func boundedText(value string, max int) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(value, "\x00", "")), " ")
	if value == "" || max == 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

type boundedWriter struct {
	builder strings.Builder
	max     int
	runes   int
}

func (writer *boundedWriter) line(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	additional := utf8.RuneCountInString(value)
	if writer.runes > 0 {
		additional++
	}
	if writer.runes+additional > writer.max {
		return false
	}
	if writer.runes > 0 {
		writer.builder.WriteByte('\n')
	}
	writer.builder.WriteString(value)
	writer.runes += additional
	return true
}

func (writer *boundedWriter) String() string { return writer.builder.String() }
