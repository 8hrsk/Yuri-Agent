package desktop

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// ActivityListInput bounds the append-only activity feed exposed to the UI.
// Cursor pagination can be added without changing the item contract.
type ActivityListInput struct {
	Limit  int    `json:"limit,omitempty"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
}

// ActivityView contains redacted audit metadata plus bounded deltas projected
// from immutable personality revisions. Raw prompts, evidence content, tool
// output and secrets never cross this boundary.
type ActivityView struct {
	ID         string               `json:"id"`
	Type       string               `json:"type"`
	Status     string               `json:"status"`
	Title      string               `json:"title"`
	Detail     string               `json:"detail,omitempty"`
	Source     string               `json:"source,omitempty"`
	ScheduleID string               `json:"scheduleId,omitempty"`
	RunID      string               `json:"runId,omitempty"`
	CreatedAt  string               `json:"createdAt"`
	DurationMS int64                `json:"durationMs,omitempty"`
	Reason     string               `json:"reason,omitempty"`
	Provenance string               `json:"provenance,omitempty"`
	Layer      string               `json:"layer,omitempty"`
	Operation  string               `json:"operation,omitempty"`
	Version    uint64               `json:"version,omitempty"`
	Evidence   int                  `json:"evidenceCount,omitempty"`
	Changes    []ActivityChangeView `json:"changes,omitempty"`
}

type ActivityChangeView struct {
	Key   string  `json:"key"`
	Delta float64 `json:"delta"`
}

func (b *Bridge) ListActivity(input ActivityListInput) ([]ActivityView, error) {
	ctx, cancel := b.context()
	defer cancel()
	limit := input.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	events, err := b.repositories.Audit.List(ctx, limit)
	if err != nil {
		return nil, err
	}
	result := make([]ActivityView, 0, len(events))
	for _, event := range events {
		item := activityFromAudit(event)
		if input.Type != "" && input.Type != "all" && item.Type != input.Type {
			continue
		}
		if input.Status != "" && input.Status != "all" && item.Status != input.Status {
			continue
		}
		b.enrichPersonalityActivity(ctx, event, &item)
		result = append(result, item)
	}
	return result, nil
}

func activityFromAudit(event storage.AuditEvent) ActivityView {
	action := strings.TrimSpace(event.Action)
	metadata := make(map[string]any)
	_ = json.Unmarshal([]byte(event.PayloadRedacted), &metadata)
	view := ActivityView{
		ID: string(event.ID), Type: activityKind(action), Source: string(event.Actor),
		Title: activityTitle(action), Detail: strings.TrimSpace(event.Target),
		Status: activityStatus(action, string(event.Decision)), RunID: firstNonEmpty(activityString(metadata, "job_run_id"), string(event.RunID)),
		ScheduleID: activityString(metadata, "schedule_id"), Layer: activityLayer(action), Operation: activityOperation(action),
		CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano), DurationMS: event.Duration.Milliseconds(),
		Reason: activitySafeReason(activityString(metadata, "reason")), Provenance: "audit:" + string(event.ID), Version: activityUint(metadata, "version", "to_version"),
	}
	if view.Detail == "" {
		view.Detail = "Локальное действие Yuri"
	}
	return view
}

func activitySafeReason(value string) string {
	value = strings.TrimSpace(value)
	if looksLikeSecret(value) {
		return "Причина скрыта: обнаружены чувствительные данные"
	}
	return truncateRunes(value, 512)
}

func activityString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func activityUint(metadata map[string]any, keys ...string) uint64 {
	for _, key := range keys {
		if value, ok := metadata[key].(float64); ok && value > 0 && value <= math.MaxUint64 {
			return uint64(value)
		}
	}
	return 0
}

func activityLayer(action string) string {
	switch {
	case strings.HasPrefix(action, "personalization."):
		return "owner_seed"
	case strings.HasPrefix(action, "persona."):
		return "mutable_persona"
	case strings.HasPrefix(action, "relationship."):
		return "relationship"
	case strings.HasPrefix(action, "affect."):
		return "affect"
	case strings.HasPrefix(action, "memory."):
		return "memory"
	case strings.HasPrefix(action, "schedule."), strings.HasPrefix(action, "job."), strings.HasPrefix(action, "delegation."), strings.HasPrefix(action, "peer_dialogue."):
		return "task"
	case strings.HasPrefix(action, "notification."), strings.HasPrefix(action, "proactivity."):
		return "policy"
	default:
		return "system"
	}
}

func activityOperation(action string) string {
	if index := strings.IndexByte(action, '.'); index >= 0 && index+1 < len(action) {
		return action[index+1:]
	}
	return action
}

func (b *Bridge) enrichPersonalityActivity(ctx context.Context, event storage.AuditEvent, view *ActivityView) {
	if b == nil || b.repositories == nil || view == nil || view.Version == 0 {
		return
	}
	id := domain.ID(strings.TrimSpace(event.Target))
	if id.Empty() {
		return
	}
	switch view.Layer {
	case "owner_seed":
		current, err := b.repositories.Personalization.GetVersion(ctx, id, view.Version)
		if err != nil {
			return
		}
		view.Reason = activitySafeReason(firstNonEmpty(current.Reason, view.Reason))
		view.Operation = string(current.Operation)
		if current.ParentVersion > 0 {
			if parent, parentErr := b.repositories.Personalization.GetVersion(ctx, id, current.ParentVersion); parentErr == nil {
				view.Changes = activityFloatChanges(personalizationActivityValues(parent), personalizationActivityValues(current))
			}
		}
	case "mutable_persona":
		record, err := b.repositories.Persona.GetVersionRecord(ctx, id, view.Version)
		if err != nil {
			return
		}
		view.Reason = activitySafeReason(firstNonEmpty(record.Reason, record.Persona.Reason, view.Reason))
		view.Operation = string(record.Operation)
		view.Evidence = len(firstEvidence(record.Evidence, record.Persona.Evidence))
		if record.ParentVersion > 0 {
			if parent, parentErr := b.repositories.Persona.GetVersion(ctx, id, record.ParentVersion); parentErr == nil {
				view.Changes = activityFloatChanges(parent.Traits, record.Persona.Traits)
			}
		}
	case "relationship":
		record, err := b.repositories.Relationship.GetVersionRecord(ctx, id, view.Version)
		if err != nil {
			return
		}
		view.Reason = activitySafeReason(firstNonEmpty(record.Reason, record.Relationship.Reason, view.Reason))
		view.Operation = string(record.Operation)
		view.Evidence = len(firstEvidence(record.Evidence, record.Relationship.Evidence))
		if record.ParentVersion > 0 {
			if parent, parentErr := b.repositories.Relationship.GetVersion(ctx, id, record.ParentVersion); parentErr == nil {
				view.Changes = activityFloatChanges(parent.Dimensions, record.Relationship.Dimensions)
				if delta := len(record.Relationship.Opinions) - len(parent.Opinions); delta != 0 {
					view.Changes = append(view.Changes, ActivityChangeView{Key: "opinion_count", Delta: float64(delta)})
				}
			}
		}
	case "affect":
		record, err := b.repositories.Affect.GetVersionRecord(ctx, id, view.Version)
		if err != nil {
			return
		}
		view.Reason = activitySafeReason(firstNonEmpty(record.Reason, record.State.Reason, view.Reason))
		view.Operation = string(record.Operation)
		if record.ParentVersion > 0 {
			if parent, parentErr := b.repositories.Affect.GetVersion(ctx, id, record.ParentVersion); parentErr == nil {
				view.Changes = activityFloatChanges(parent.Emotions, record.State.Emotions)
			}
		}
		if events, eventsErr := b.repositories.Affect.ListEvents(ctx, id); eventsErr == nil {
			for _, affectEvent := range events {
				if affectEvent.StateVersion == view.Version {
					view.Evidence += len(affectEvent.Evidence)
				}
			}
		}
	}
}

func personalizationActivityValues(seed domain.PersonalizationSeed) map[string]float64 {
	values := make(map[string]float64)
	for key, value := range seed.Temperament.Traits() {
		values["trait."+key] = value
	}
	style := seed.CommunicationStyle
	values["style.verbosity"], values["style.softness"], values["style.humor"] = style.Verbosity, style.Softness, style.Humor
	values["style.expressiveness"], values["style.supportiveness"], values["style.formality"] = style.Expressiveness, style.Supportiveness, style.Formality
	dynamics := seed.EmotionalDynamics
	values["dynamics.reactivity"], values["dynamics.response_intensity"] = dynamics.Reactivity, dynamics.ResponseIntensity
	values["dynamics.recovery_speed"], values["dynamics.expression"], values["dynamics.masking"] = dynamics.RecoverySpeed, dynamics.Expression, dynamics.Masking
	for key, value := range seed.RelationshipSeed.Dimensions {
		values["relationship."+key] = value
	}
	values["backstory.episodes"] = float64(len(seed.Backstory.Episodes))
	return values
}

func activityFloatChanges(previous, next map[string]float64) []ActivityChangeView {
	keys := make(map[string]struct{}, len(previous)+len(next))
	for key := range previous {
		keys[key] = struct{}{}
	}
	for key := range next {
		keys[key] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	result := make([]ActivityChangeView, 0, len(ordered))
	for _, key := range ordered {
		if delta := next[key] - previous[key]; math.Abs(delta) >= .001 {
			result = append(result, ActivityChangeView{Key: key, Delta: delta})
		}
	}
	return result
}

func activityKind(action string) string {
	switch {
	case strings.HasPrefix(action, "schedule."), strings.HasPrefix(action, "job."):
		return "job"
	case strings.HasPrefix(action, "delegation."):
		return "job"
	case strings.HasPrefix(action, "peer_dialogue."):
		return "job"
	case strings.HasPrefix(action, "notification."), strings.HasPrefix(action, "proactivity."):
		return "proactive"
	case strings.HasPrefix(action, "plugin."):
		return "system"
	case strings.HasPrefix(action, "backup."):
		return "system"
	case strings.HasPrefix(action, "memory."):
		return "memory"
	case strings.HasPrefix(action, "personalization."), strings.HasPrefix(action, "persona."), strings.HasPrefix(action, "relationship."), strings.HasPrefix(action, "affect."):
		return "reflection"
	case strings.HasPrefix(action, "tool."):
		return "system"
	default:
		return "system"
	}
}

func activityTitle(action string) string {
	switch action {
	case "schedule.create":
		return "Создано расписание"
	case "schedule.update":
		return "Расписание изменено"
	case "schedule.pause":
		return "Расписание приостановлено"
	case "schedule.resume":
		return "Расписание возобновлено"
	case "schedule.delete":
		return "Расписание удалено"
	case "job.started":
		return "Фоновая задача запущена"
	case "job.completed":
		return "Фоновая задача завершена"
	case "job.failed":
		return "Фоновая задача завершилась с ошибкой"
	case "delegation.created":
		return "Субагент создан"
	case "delegation.started":
		return "Субагент приступил к задаче"
	case "delegation.completed":
		return "Субагент завершил задачу"
	case "delegation.failed":
		return "Субагент завершился с ошибкой"
	case "delegation.cancelled":
		return "Субагент остановлен"
	case "peer_dialogue.queued":
		return "Межагентный диалог поставлен в очередь"
	case "peer_dialogue.auto_queued":
		return "Агент самостоятельно запросил мнение peer"
	case "peer_dialogue.auto_blocked":
		return "Автономный диалог заблокирован политикой"
	case "peer_dialogue.auto_no_change":
		return "Агент решил не начинать внутренний диалог"
	case "peer_dialogue.started":
		return "Агенты начали внутренний диалог"
	case "peer_dialogue.completed":
		return "Внутренний диалог агентов завершён"
	case "peer_dialogue.failed":
		return "Внутренний диалог завершился с ошибкой"
	case "peer_dialogue.cancelled":
		return "Внутренний диалог остановлен"
	case "peer_dialogue.expired":
		return "Время внутреннего диалога истекло"
	case "notification.sent":
		return "Отправлено уведомление"
	case "notification.deferred":
		return "Уведомление отложено"
	case "notification.suppressed":
		return "Уведомление подавлено политикой"
	case "persona.reflection":
		return "Личность Yuri изменилась после рефлексии"
	case "persona.update":
		return "Личность Yuri изменилась"
	case "relationship.update":
		return "Представление Yuri об отношениях изменилось"
	case "affect.update", "affect.event", "affect.decay":
		return "Эмоциональное состояние Yuri изменилось"
	case "persona.rollback":
		return "Версия личности восстановлена"
	case "persona.reset":
		return "Личность сброшена к исходному профилю"
	case "persona.trait_pin", "persona.pin":
		return "Закрепление черты изменено"
	case "persona.auto_evolution":
		return "Режим автоэволюции изменён"
	case "personalization.owner_seed.update":
		return "Владелец изменил исходную персонализацию"
	case "backup.create":
		return "Создана зашифрованная резервная копия"
	case "backup.validate":
		return "Резервная копия проверена"
	case "backup.restore":
		return "Резервная копия восстановлена отдельно"
	default:
		if action == "" {
			return "Системное событие"
		}
		return action
	}
}

func activityStatus(action, decision string) string {
	switch action {
	case "job.queued":
		return "queued"
	case "job.started":
		return "running"
	case "delegation.created":
		return "queued"
	case "peer_dialogue.queued", "peer_dialogue.auto_queued":
		return "queued"
	case "peer_dialogue.auto_blocked", "peer_dialogue.auto_no_change":
		return "skipped"
	case "delegation.started":
		return "running"
	case "peer_dialogue.started":
		return "running"
	case "job.completed", "notification.sent":
		return "completed"
	case "delegation.completed":
		return "completed"
	case "peer_dialogue.completed":
		return "completed"
	case "job.failed", "notification.failed":
		return "failed"
	case "delegation.failed":
		return "failed"
	case "peer_dialogue.failed":
		return "failed"
	case "job.cancelled":
		return "cancelled"
	case "delegation.cancelled":
		return "cancelled"
	case "peer_dialogue.cancelled":
		return "cancelled"
	case "peer_dialogue.expired":
		return "skipped"
	case "job.skipped", "notification.deferred", "notification.suppressed":
		return "skipped"
	}
	switch decision {
	case "deny":
		return "blocked"
	case "needs_approval":
		return "queued"
	default:
		return "info"
	}
}
