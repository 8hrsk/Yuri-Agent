package sqlite

import (
	"fmt"
	"time"

	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func parseAffectEventArgs(args ...any) (id domain.ID, expected uint64, event domain.AffectiveEvent, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case domain.ID:
			if id.Empty() {
				id = item
			}
		case string:
			if id.Empty() {
				id = domain.ID(item)
			}
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case domain.AffectiveEvent:
			event = item
		case *domain.AffectiveEvent:
			if item == nil {
				err = fmt.Errorf("%w: nil affect event", domain.ErrInvalidArgument)
			} else {
				event = *item
			}
		default:
			err = fmt.Errorf("%w: unsupported affect event argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	if id.Empty() && !event.AffectID.Empty() {
		id = event.AffectID
	}
	if id.Empty() {
		err = fmt.Errorf("%w: affect state id is required", domain.ErrInvalidArgument)
	}
	return
}

func parseAffectDecayArgs(args ...any) (expected uint64, at time.Time, reason string, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case time.Time:
			at = item
		case string:
			reason = item
		default:
			err = fmt.Errorf("%w: unsupported affect decay argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	return
}

func parseAffectRevisionArgs(args ...any) (expected, target uint64, reason string, at time.Time, err error) {
	versions := make([]uint64, 0, 2)
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			versions = append(versions, item)
		case uint:
			versions = append(versions, uint64(item))
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				versions = append(versions, uint64(item))
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported affect rollback argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	if err == nil {
		switch len(versions) {
		case 0:
		case 1:
			target = versions[0]
		case 2:
			expected, target = versions[0], versions[1]
		default:
			err = fmt.Errorf("%w: too many affect versions", domain.ErrInvalidArgument)
		}
	}
	return
}

func parseAffectResetArgs(args ...any) (expected uint64, reason string, at time.Time, err error) {
	for _, value := range args {
		switch item := value.(type) {
		case uint64:
			expected = item
		case uint:
			expected = uint64(item)
		case int:
			if item < 0 {
				err = fmt.Errorf("%w: affect version cannot be negative", domain.ErrInvalidArgument)
			} else {
				expected = uint64(item)
			}
		case string:
			reason = item
		case time.Time:
			at = item
		default:
			err = fmt.Errorf("%w: unsupported affect reset argument %T", domain.ErrInvalidArgument, value)
		}
		if err != nil {
			return
		}
	}
	return
}
