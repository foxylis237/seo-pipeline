package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// PipelineConfig — чем полный прогон занимается помимо генерации текста.
//
// Здесь только шаги, которые уходят наружу: в блог и в чужой Google Drive. Сами стадии
// генерации отсюда не управляются и не должны: их порядок раннер выводит из сохранённых
// артефактов статьи, а не из списка в файле, — выключенная посередине стадия означала бы
// статью, которую невозможно достроить.
//
// Живёт в конфиге задачи рядом с её стадиями: у каждой задачи свой файл, и решение
// «выкладываем или пока нет» принимается для неё отдельно.
type PipelineConfig struct {
	// PublishAfterRun — публиковать ли статью в WordPress сразу после успешного прогона.
	//
	// Умолчание — не публиковать, и это не осторожность ради осторожности: публикация
	// необратима, записи в блоге приложение удалять не умеет, а `run` человек запускает ради
	// текста. Отдельная команда publish остаётся доступной при любом значении.
	PublishAfterRun bool `yaml:"publish_after_run"`
	// GoogleDocs — выгружать ли промпт статьи в Google Docs по ходу прогона.
	//
	// Умолчание — выгружать: так работали задачи до появления этой секции. Отказ Google
	// генерацию не роняет ни при каком значении.
	GoogleDocs bool `yaml:"google_docs"`
}

// DefaultPipelineConfig — поведение задачи, которая секцию не объявила.
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{PublishAfterRun: false, GoogleDocs: true}
}

// LoadPipelineConfig читает секцию pipeline из конфига задачи.
//
// Файл тот же, что и у стадий: задача описывается одним файлом, и второго места, куда надо
// заглянуть, чтобы понять её поведение, заводить незачем. Отсутствующая секция и отсутствующий
// ключ внутри неё одинаково означают умолчание — так добавление выключателя не меняет
// поведение задач, которые о нём не знают.
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
