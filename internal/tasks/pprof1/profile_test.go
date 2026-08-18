package pprof1

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/config"
)

// Бюджет стадии обязан вмещать не одну попытку: отказ провайдера лечится повтором, а повтор
// не начнётся, пока первая попытка забирает бюджет целиком. Проверяется по живому файлу
// задачи — расхождение здесь и есть стадия, которая падает, ни разу не повторившись.
func TestStageBudgetsLeaveRoomForRetries(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)

	profile := Profile()
	cfg, err := config.LoadLLMConfigForStages(profile.LLMConfigPath, profile.LLMStages, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range profile.LLMStages {
		// Промпт статьи в модель не уходит — повторять на этой стадии нечего.
		if name == StageArticle {
			continue
		}
		stage := cfg.Stages[name]
		if stage.AttemptTimeout >= stage.Timeout {
			t.Fatalf("стадия %s: attempt_timeout %v не короче бюджета %v", name, stage.AttemptTimeout, stage.Timeout)
		}
		// Перегруженного провайдера пережидают минуту и три: без этого запаса повторы
		// упрутся в конец бюджета раньше, чем отказ успеет пройти.
		if spare := stage.Timeout - stage.AttemptTimeout; spare < 4*time.Minute {
			t.Fatalf("стадия %s: на паузы между повторами остаётся %v", name, spare)
		}
	}
}
