package sqlite

import (
	"fmt"
	"strings"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func validateScheduleForStorage(schedule domain.Schedule) error {
	if err := schedule.ValidateBasic(); err != nil {
		return err
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("%w: invalid schedule timezone: %v", domain.ErrInvalidArgument, err)
	}
	if err := validJSON(schedule.PayloadJSON, "payload_json"); err != nil {
		return err
	}
	switch schedule.Kind {
	case domain.ScheduleKindOnce:
		if schedule.Expression != "" || schedule.IntervalSeconds != 0 {
			return fmt.Errorf("%w: one-shot schedule has interval or expression", domain.ErrInvalidArgument)
		}
	case domain.ScheduleKindInterval:
		if schedule.Expression != "" || schedule.IntervalSeconds <= 0 {
			return fmt.Errorf("%w: interval schedule requires interval_seconds only", domain.ErrInvalidArgument)
		}
	case domain.ScheduleKindCron:
		if strings.TrimSpace(schedule.Expression) == "" || schedule.IntervalSeconds != 0 {
			return fmt.Errorf("%w: cron schedule requires expression only", domain.ErrInvalidArgument)
		}
	}
	return nil
}

func normalizeSchedule(schedule domain.Schedule) domain.Schedule {
	schedule.Name = strings.TrimSpace(schedule.Name)
	schedule.Timezone = strings.TrimSpace(schedule.Timezone)
	schedule.Expression = strings.TrimSpace(schedule.Expression)
	schedule.PayloadJSON = strings.TrimSpace(schedule.PayloadJSON)
	if schedule.PayloadJSON == "" {
		schedule.PayloadJSON = "{}"
	}
	schedule.StartAt = schedule.StartAt.UTC()
	schedule.NextRunAt = schedule.NextRunAt.UTC()
	schedule.LastRunAt = schedule.LastRunAt.UTC()
	schedule.CreatedAt = schedule.CreatedAt.UTC()
	schedule.UpdatedAt = schedule.UpdatedAt.UTC()
	return schedule
}

func validateScheduledClaim(claim domain.ScheduledClaim) error {
	if claim.ScheduleID.Empty() || claim.ExpectedVersion == 0 || claim.ScheduledFor.IsZero() || claim.Now.IsZero() ||
		strings.TrimSpace(claim.WorkerID) == "" || claim.LeaseDuration <= 0 || claim.LeaseDuration > 30*24*time.Hour {
		return fmt.Errorf("%w: invalid scheduled claim", domain.ErrInvalidArgument)
	}
	if claim.ScheduledFor.After(claim.Now) {
		return fmt.Errorf("%w: scheduled occurrence is in the future", domain.ErrInvalidArgument)
	}
	return nil
}

func validateMisfireRecord(record domain.MisfireRecord) error {
	if record.ScheduleID.Empty() || record.ExpectedVersion == 0 || record.ScheduledFor.IsZero() || record.Now.IsZero() ||
		record.ScheduledFor.After(record.Now) {
		return fmt.Errorf("%w: invalid misfire record", domain.ErrInvalidArgument)
	}
	return nil
}

func validateManualRunRequest(request domain.ManualRunRequest) error {
	if request.ScheduleID.Empty() || request.Now.IsZero() {
		return fmt.Errorf("%w: invalid manual run request", domain.ErrInvalidArgument)
	}
	return nil
}

func validateRetryClaim(request domain.RetryClaim) error {
	if request.RunID.Empty() || request.Now.IsZero() || strings.TrimSpace(request.WorkerID) == "" ||
		request.LeaseDuration <= 0 || request.LeaseDuration > 30*24*time.Hour {
		return fmt.Errorf("%w: invalid retry claim", domain.ErrInvalidArgument)
	}
	return nil
}

func validateRenewLeaseRequest(request domain.RenewLeaseRequest) error {
	if request.RunID.Empty() || strings.TrimSpace(request.WorkerID) == "" || strings.TrimSpace(request.LeaseToken) == "" ||
		request.Now.IsZero() || request.LeaseUntil.IsZero() || !request.LeaseUntil.After(request.Now) {
		return fmt.Errorf("%w: invalid lease renewal", domain.ErrInvalidArgument)
	}
	return nil
}

func validateCompletionRequest(request domain.CompleteRunRequest) error {
	if request.RunID.Empty() || strings.TrimSpace(request.WorkerID) == "" || strings.TrimSpace(request.LeaseToken) == "" || request.Now.IsZero() {
		return fmt.Errorf("%w: invalid completion request", domain.ErrInvalidArgument)
	}
	return nil
}

func validateFailureRequest(request domain.FailRunRequest) error {
	if request.RunID.Empty() || strings.TrimSpace(request.WorkerID) == "" || strings.TrimSpace(request.LeaseToken) == "" ||
		request.Now.IsZero() || strings.TrimSpace(request.Reason) == "" {
		return fmt.Errorf("%w: invalid failure request", domain.ErrInvalidArgument)
	}
	return nil
}

func validateCancelRequest(request domain.CancelRunRequest) error {
	if request.RunID.Empty() || request.Now.IsZero() {
		return fmt.Errorf("%w: invalid cancellation request", domain.ErrInvalidArgument)
	}
	return nil
}
