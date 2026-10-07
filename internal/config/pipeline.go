package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// PipelineConfig — внешние шаги полного прогона помимо генерации текста.
// Стадии генерации отсюда не управляются: их порядок раннер выводит из артефактов статьи.
type PipelineConfig struct {
	// PublishAfterRun — публиковать ли статью в WordPress после успешного run и regenerate.
	PublishAfterRun bool `yaml:"publish_after_run"`
	// GoogleDocs — выгружать ли промпт статьи в Google Docs по ходу прогона.
	GoogleDocs bool `yaml:"google_docs"`
	// KeysSO — собирать ли запросы конкурента через Keys.so; без него запросы подбирает модель.
	KeysSO bool `yaml:"keysso"`
}

// DefaultPipelineConfig — поведение задачи, которая секцию не объявила.
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{PublishAfterRun: false, GoogleDocs: true, KeysSO: true}
}

// LoadPipelineConfig читает секцию pipeline из конфига задачи.
// Отсутствующая секция и отсутствующий ключ одинаково означают умолчание.
func LoadPipelineConfig(path string) (PipelineConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return PipelineConfig{}, fmt.Errorf("прочитать конфиг %s: %w", path, err)
	}
	file := struct {
		Pipeline PipelineConfig `yaml:"pipeline"`
	}{Pipeline: DefaultPipelineConfig()}
	if err := yaml.Unmarshal(data, &file); err != nil {
		return PipelineConfig{}, fmt.Errorf("разобрать конфиг %s: %w", path, err)
	}
	return file.Pipeline, nil
}
