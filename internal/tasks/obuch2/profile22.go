package obuch2

import "github.com/foxylis237/seo-pipeline/internal/tasks"

// obuch_22 is obuch_2 without Arsenkin: same site, flow and publishing, its own prompts,
// schema and workbook.
const (
	Name22    = "obuch_22"
	Command22 = "obuch-22"
)

// ExportPrompt22Path is the export prompt template of obuch_22.
const ExportPrompt22Path = "tasks/obuch_22/templates/article_prompt_export.txt"

// Profile22 returns the obuch_22 configuration.
func Profile22() tasks.Profile {
	profile := Profile()
	profile.Name = Name22
	profile.Command = Command22
	profile.InputDir = "input/obuch_22"
	profile.OutputDir = "tasks/obuch_22/output"
	profile.PromptsDir = "tasks/obuch_22/prompts"
	profile.TemplatePath = "tasks/obuch_22/templates/result.md.tmpl"
	profile.LLMConfigPath = "config/obuch_22.yaml"
	profile.ImportReportsDir = "output/obuch_22/import-reports"
	profile.DiagnosticsDir = "output/obuch_22/debug"
	profile.DBSchema = Name22
	return profile
}
