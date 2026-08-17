package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/pipeline/article"
	"github.com/foxylis237/seo-pipeline/internal/pipeline/importer"
)

type articleImporter interface {
	Import(context.Context, article.Input) (article.Article, bool, error)
}

func runImport(
	ctx context.Context,
	articleRepository articleImporter,
	inputFilePath string,
	reportDirectory string,
	limit int,
	logger *slog.Logger,
) error {
	startedAt := time.Now().UTC()
	report := importer.Report{
		StartedAt: startedAt,
		InputFile: inputFilePath,
		Limit:     limit,
		Errors:    make([]importer.RowError, 0),
	}
	rows, readErr := importer.ReadRows(inputFilePath)
	var importErr error
	if readErr != nil {
		importErr = fmt.Errorf("прочитать Excel: %w", readErr)
		report.FatalError = importErr.Error()
	}

	for _, row := range rows {
		report.Summary.Viewed++
		if row.Empty {
			report.Summary.Empty++
			continue
		}
		if len(row.Errors) > 0 {
			report.Summary.Invalid++
			report.Errors = append(report.Errors, importer.RowError{
				ExcelRow: row.Number, ExternalID: row.ExternalID, Title: row.Title,
				Errors: append([]string(nil), row.Errors...), Time: time.Now().UTC(),
			})
			continue
		}

		createdArticle, created, err := articleRepository.Import(ctx, row.Input)
		if err != nil {
			importErr = fmt.Errorf(
				"сохранить статью из Excel с ID %d и названием %q: %w",
				row.Input.ExcelID,
				row.Input.Title,
				err,
			)
			report.FatalError = importErr.Error()
			break
		}

		if !created {
			report.Summary.Existing++
			logger.Debug(
				"ранее импортированная статья пропущена",
				"article_id", createdArticle.ID,
				"external_id", createdArticle.ExternalID,
				"title", createdArticle.Title,
			)
			continue
		}
		report.Summary.Imported++
		logger.Info(
			"новая статья импортирована",
			"article_id", createdArticle.ID,
			"external_id", createdArticle.ExternalID,
			"title", row.Input.Title,
		)
		if limit > 0 && report.Summary.Imported >= limit {
			report.Summary.LimitReached = true
			break
		}
	}

	report.FinishedAt = time.Now().UTC()
	reportPath, reportErr := importer.SaveReport(reportDirectory, report)
	logger.Info(
		"импорт статей завершён",
		"viewed_count", report.Summary.Viewed,
		"imported_count", report.Summary.Imported,
		"existing_count", report.Summary.Existing,
		"invalid_count", report.Summary.Invalid,
		"empty_count", report.Summary.Empty,
		"limit_reached", report.Summary.LimitReached,
		"report_path", reportPath,
	)
	return errors.Join(importErr, reportErr)
}
