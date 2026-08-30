package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/foxylis237/seo-pipeline/internal/integrations/wordpress"
)

// maxPublishSystemFailures — сколько подряд отказов площадки выключают публикацию в прогоне.
//
// Три, а не один: единичный 502 или оборванное соединение случаются и на живой площадке, а
// выключаться от каждого чиха — значит требовать второго запуска там, где хватило бы паузы.
// И не десять: если площадка легла, десять статей подряд будут ждать таймаут впустую, а
// генерация тем временем стоит.
const maxPublishSystemFailures = 3

// newRunPublisherFor собирает публикатор прогона по настройкам задачи.
//
// Живёт здесь, а не в main(): та и без того за порогом сложности, а решений тут три —
// просили ли публиковать, настроена ли площадка и что делать, если нет.
//
// Не настроенная площадка прогон не останавливает. Человек запускал генерацию, она стоит
// денег и времени, и отказывать в ней из-за пустой строки в .env было бы обменом дорогого
// на дешёвое: об этом говорится один раз, до первой статьи, и прогон идёт без публикации.
func newRunPublisherFor(deps wordPressCommandDeps, enabled bool) *runPublisher {
	if !enabled {
		return newRunPublisher(nil, deps.logger)
	}
	publish, err := newWordPressRunPublish(deps)
	if err != nil {
		deps.logger.Warn("публикация после прогона выключена: площадка не настроена",
			"stage", "wordpress_publish", "error", err)
		publisher := newRunPublisher(nil, deps.logger)
		publisher.disable("площадка не настроена")
		return publisher
	}
	return newRunPublisher(publish, deps.logger)
}

// publishOutcome — чем закончилась попытка опубликовать одну статью.
type publishOutcome string

const (
	publishOutcomePublished publishOutcome = "published"
	// publishOutcomeSkipped — статья не опубликована из-за своих данных: нет обложки, нет
	// метки в блоге, не заполнено обязательное поле, статья уже есть в блоге.
	publishOutcomeSkipped publishOutcome = "skipped"
	// publishOutcomeFailed — отказ площадки: таймаут, 401, 5xx, непригодный ответ.
	publishOutcomeFailed publishOutcome = "failed"
	// publishOutcomeDisabled — публикация в этом прогоне уже выключена или не включалась.
	publishOutcomeDisabled publishOutcome = "disabled"
)

// runPublisher публикует статью, которую прогон только что довёл до конца.
//
// Существует ради одного правила: генерация не зависит от WordPress. Метод публикации
// ошибок наружу не отдаёт вовсе — прогон обязан идти дальше, чем бы публикация ни
// закончилась. За генерацию уже заплачено, и отказ площадки не имеет права её отменить.
//
// Второе правило — не долбиться в лежащую площадку. Отказы площадки считаются подряд, и
// после maxPublishSystemFailures публикация в этом прогоне выключается: остальные статьи
// всё равно проходят все этапы генерации, а публикацию им делает отдельная команда publish,
// когда площадка починится.
type runPublisher struct {
	publish func(ctx context.Context, externalID string) error
	logger  *slog.Logger

	// enabled — включена ли публикация в этом прогоне вообще.
	enabled bool
	// disabledReason непуст, если публикацию выключили по ходу прогона.
	disabledReason string
	// failStreak — сколько отказов площадки подряд. Успех обнуляет его: раз площадка
	// ответила, предыдущие отказы были временными.
	failStreak int

	published []string
	skipped   []string
	failed    []string
}

// newRunPublisher собирает публикатор для прогона.
//
// publish нулевой означает выключенную публикацию — тогда каждая статья получает отметку
// skipped, а прогон идёт как раньше. Это же состояние остаётся, когда площадка не
// настроена: об этом сказано один раз в начале, а не по разу на статью.
func newRunPublisher(publish func(context.Context, string) error, logger *slog.Logger) *runPublisher {
	return &runPublisher{publish: publish, logger: logger, enabled: publish != nil}
}

// wrap добавляет публикацию последним этапом полного прогона.
//
// Ошибка генерации до публикации не доходит: публиковать недоделанную статью нечего, а
// сама публикация ошибку наружу не отдаёт — прогон обязан продолжаться независимо от
// состояния площадки.
//
// Оборачивается и run, и regenerate: пересоздание — тот же полный прогон, только со сбросом
// артефактов перед ним и проверкой итога после. Обёртка у regenerate ставится поверх всего
// пересоздания, вместе с этой проверкой, — иначе публикация ушла бы раньше, чем выяснится,
// достроена ли статья.
func (p *runPublisher) wrap(runOne func(context.Context, string) error) func(context.Context, string) error {
	return func(ctx context.Context, externalID string) error {
		if err := runOne(ctx, externalID); err != nil {
			return err
		}
		p.publishAfterRun(ctx, externalID)
		return nil
	}
}

// publishAfterRun публикует статью, завершившую полный прогон.
//
// Ошибку не возвращает намеренно: см. комментарий к типу. Что именно произошло, видно в
// логе и в итоге прогона.
func (p *runPublisher) publishAfterRun(ctx context.Context, externalID string) publishOutcome {
	if p == nil {
		return publishOutcomeDisabled
	}
	logger := p.logger.With("external_id", externalID, "stage", "wordpress_publish")
	if p.publish == nil && p.disabledReason == "" {
		// Публикацию не просили: прогон запущен как раньше, ради текста. Ни отметки, ни
		// строчки в итоге — вывод обычного прогона от появления этого этапа не меняется.
		return publishOutcomeDisabled
	}
	if !p.enabled {
		logger.Info("публикация пропущена",
			"outcome", string(publishOutcomeDisabled), "reason", p.disabledReason)
		p.skipped = append(p.skipped, externalID)
		return publishOutcomeDisabled
	}

	err := p.publish(ctx, externalID)
	switch {
	case err == nil:
		// Успех обнуляет счётчик: площадка отвечает, значит предыдущие отказы были временными.
		p.failStreak = 0
		p.published = append(p.published, externalID)
		return publishOutcomePublished

	case isGracefulCancellation(ctx, err):
		// Прогон и так заканчивается, и выключать публикацию из-за собственной отмены незачем.
		logger.Info("публикация прервана вместе с прогоном", "outcome", string(publishOutcomeSkipped))
		p.skipped = append(p.skipped, externalID)
		return publishOutcomeSkipped

	case wordpress.IsSystemFailure(err):
		p.failStreak++
		p.failed = append(p.failed, externalID)
		logger.Error("публикация не удалась: отказ площадки",
			"outcome", string(publishOutcomeFailed), "fail_streak", p.failStreak, "error", err)
		if p.failStreak >= maxPublishSystemFailures {
			p.enabled = false
			p.disabledReason = fmt.Sprintf("подряд отказов площадки: %d", p.failStreak)
			logger.Error("публикация в этом прогоне выключена",
				"reason", p.disabledReason,
				"hint", "генерация продолжится; опубликовать оставшееся — командой publish")
		}
		return publishOutcomeFailed

	default:
		// Данные конкретной статьи: нет обложки, нет метки в блоге, не заполнено поле,
		// статья уже есть в блоге. Соседние статьи здесь ни при чём, и счётчик отказов
		// площадки такие случаи не трогают.
		logger.Warn("публикация пропущена: статья не годна",
			"outcome", string(publishOutcomeSkipped), "error", err)
		p.skipped = append(p.skipped, externalID)
		return publishOutcomeSkipped
	}
}

// printSummary подводит итог публикации за прогон.
//
// Печатается всегда, когда публикация включалась: молчание после прогона означало бы, что
// человек узнает о невыложенных статьях из блога, а не из вывода команды.
func (p *runPublisher) printSummary(out io.Writer) {
	// Молчание, когда публикацию не просили: обычный прогон выглядит как раньше.
	if p == nil || (p.publish == nil && p.disabledReason == "") {
		return
	}
	fmt.Fprintf(out, "\nПубликация в WordPress: опубликовано %d, пропущено %d, отказов площадки %d.\n",
		len(p.published), len(p.skipped), len(p.failed))
	if len(p.published) > 0 {
		fmt.Fprintf(out, "  опубликованы: %s\n", strings.Join(p.published, ", "))
	}
	if len(p.failed) > 0 {
		fmt.Fprintf(out, "  отказ площадки: %s\n", strings.Join(p.failed, ", "))
	}
	if len(p.skipped) > 0 {
		fmt.Fprintf(out, "  не опубликованы: %s\n", strings.Join(p.skipped, ", "))
	}
	if p.disabledReason != "" {
		fmt.Fprintf(out, "Публикация выключена по ходу прогона (%s): статьи сгенерированы полностью,\n"+
			"опубликовать оставшееся можно командой publish, когда площадка ответит.\n", p.disabledReason)
	}
}

// disable выключает публикацию до начала прогона — например, когда площадка не настроена.
func (p *runPublisher) disable(reason string) {
	p.enabled = false
	p.disabledReason = reason
}
