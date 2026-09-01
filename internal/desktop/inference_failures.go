package desktop

import (
	"fmt"
	"strings"

	"github.com/OrdoAI/yuri-agent/internal/agent"
	"github.com/OrdoAI/yuri-agent/internal/domain"
)

func inferenceFailure(cause error) (string, domain.RunFailureInfo) {
	info := agent.DurableFailureInfo(cause)
	return inferenceFailureMessage(info, safeError(errorString(cause))), info
}

func inferenceFailureMessage(info domain.RunFailureInfo, fallback string) string {
	switch info.Kind {
	case domain.RunFailureAuthentication:
		return "Авторизация провайдера недоступна"
	case domain.RunFailureRateLimit:
		if info.RetryAfterSeconds > 0 {
			return fmt.Sprintf("Провайдер ограничил частоту запросов; повторите через %d сек.", info.RetryAfterSeconds)
		}
		return "Провайдер ограничил частоту запросов; повторите позже"
	case domain.RunFailureQuotaExhausted:
		return "Лимит или баланс провайдера исчерпан"
	case domain.RunFailureContextLimit:
		return "Контекст превышает лимит выбранной модели"
	case domain.RunFailureModelUnavailable:
		return "Выбранная модель недоступна у провайдера"
	case domain.RunFailureTimeout:
		return "Провайдер не ответил вовремя"
	case domain.RunFailureTransient:
		return "Провайдер временно недоступен"
	case domain.RunFailureInvalidRequest:
		return "Провайдер отклонил параметры запроса"
	case domain.RunFailureBudgetExceeded:
		return "Запуск достиг установленного лимита"
	case domain.RunFailureUnknown:
		return "Операция провайдера завершилась ошибкой"
	default:
		return fallback
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return strings.TrimSpace(err.Error())
}
