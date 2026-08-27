package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writePipelineConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "task.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Задача, которая секцию не объявила, работает как раньше: промпт уходит в Google Docs,
// а в блог прогон сам ничего не выкладывает.
func TestLoadPipelineConfigFallsBackToDefaults(t *testing.T) {
	path := writePipelineConfig(t, "llm:\n  stages: {}\n")
	cfg, err := LoadPipelineConfig(path)
	if err != nil {
		t.Fatalf("LoadPipelineConfig: %v", err)
	}
	if cfg != DefaultPipelineConfig() {
		t.Fatalf("умолчания разошлись: %+v", cfg)
	}
	if cfg.PublishAfterRun {
		t.Fatal("публикация после прогона включена по умолчанию — она необратима")
	}
}

// Ключ, которого нет внутри объявленной секции, тоже остаётся умолчанием: выключатель,
// добавленный завтра, не должен менять поведение задач, которые о нём не знают.
func TestLoadPipelineConfigKeepsDefaultForMissingKey(t *testing.T) {
	path := writePipelineConfig(t, "pipeline:\n  publish_after_run: true\n")
	cfg, err := LoadPipelineConfig(path)
	if err != nil {
		t.Fatalf("LoadPipelineConfig: %v", err)
	}
	if !cfg.PublishAfterRun {
		t.Fatal("объявленный выключатель не прочитан")
	}
	if !cfg.GoogleDocs {
		t.Fatal("необъявленный ключ потерял умолчание")
	}
}

func TestLoadPipelineConfigReadsBothSwitches(t *testing.T) {
	path := writePipelineConfig(t, "pipeline:\n  publish_after_run: false\n  google_docs: false\n")
	cfg, err := LoadPipelineConfig(path)
	if err != nil {
		t.Fatalf("LoadPipelineConfig: %v", err)
	}
	if cfg.PublishAfterRun || cfg.GoogleDocs {
		t.Fatalf("выключатели не прочитаны: %+v", cfg)
	}
}

// Боевые конфиги задач читаются тем же кодом: опечатка в отступе или в имени ключа обязана
// падать здесь, а не через два часа генерации. Ожидаемое значение выключателя записано рядом
// с конфигом намеренно: публикация после прогона пишет в живой блог, и включиться она имеет
// право только осознанно — незамеченная правка YAML падает здесь.
func TestTaskConfigsDeclarePipelineSection(t *testing.T) {
	cases := map[string]bool{
		filepath.Join("..", "..", "config", "config.yaml"):  false,
		filepath.Join("..", "..", "config", "pprof_1.yaml"): false,
		// pprof_2 публикует страницу сразу после прогона — включено по просьбе владельца задачи.
		filepath.Join("..", "..", "config", "pprof_2.yaml"): true,
	}
	for path, wantPublish := range cases {
		cfg, err := LoadPipelineConfig(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if cfg.PublishAfterRun != wantPublish {
			t.Fatalf("%s: publish_after_run = %v, ожидалось %v", path, cfg.PublishAfterRun, wantPublish)
		}
		if !cfg.GoogleDocs {
			t.Fatalf("%s: выгрузка промпта выключена, а этого никто не просил", path)
		}
	}
}
