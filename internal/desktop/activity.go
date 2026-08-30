package desktop

import (
	"encoding/json"
	"strings"
	"time"

	storage "github.com/OrdoAI/yuri-agent/internal/storage/sqlite"
)

// ActivityListInput bounds the append-only activity feed exposed to the UI.
// Cursor pagination can be added without changing the item contract.
type ActivityListInput struct {
	Limit  int    `json:"limit,omitempty"`
	Type   string `json:"type,omitempty"`
	Status string `json:"status,omitempty"`
}

// ActivityView deliberately contains only redacted audit metadata. Raw model
// requests, tool output and secrets never cross this boundary.
type ActivityView struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Status     string `json:"status"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	Source     string `json:"source,omitempty"`
	ScheduleID string `json:"scheduleId,omitempty"`
	RunID      string `json:"runId,omitempty"`
	CreatedAt  string `json:"createdAt"`
	DurationMS int64  `json:"durationMs,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Provenance string `json:"provenance,omitempty"`
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
		result = append(result, item)
	}
	return result, nil
}

func activityFromAudit(event storage.AuditEvent) ActivityView {
	action := strings.TrimSpace(event.Action)
	metadata := make(map[string]string)
	_ = json.Unmarshal([]byte(event.PayloadRedacted), &metadata)
	view := ActivityView{
		ID: string(event.ID), Type: activityKind(action), Source: string(event.Actor),
		Title: activityTitle(action), Detail: strings.TrimSpace(event.Target),
		Status: activityStatus(action, string(event.Decision)), RunID: firstNonEmpty(metadata["job_run_id"], string(event.RunID)),
		ScheduleID: metadata["schedule_id"],
		CreatedAt:  event.CreatedAt.UTC().Format(time.RFC3339Nano), DurationMS: event.Duration.Milliseconds(),
		Reason: metadata["reason"], Provenance: "audit:" + string(event.ID),
	}
	if view.Detail == "" {
		view.Detail = "Локальное действие Yuri"
	}
	return view
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
	case strings.HasPrefix(action, "persona."), strings.HasPrefix(action, "relationship."), strings.HasPrefix(action, "affect."):
		return "memory"
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
