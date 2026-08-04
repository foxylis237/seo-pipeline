package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/foxylis237/seo-pipeline/internal/tasks/task1/article"
)

type currentErrorsRepository interface {
	ListArticlesWithErrors(context.Context) ([]article.ArticleError, error)
	GetArticleByExternalID(context.Context, string) (article.Article, error)
}

func runErrors(ctx context.Context, repository currentErrorsRepository, externalID string, output io.Writer) error {
	var records []article.ArticleError
	var err error
	if externalID == "" {
		records, err = repository.ListArticlesWithErrors(ctx)
	} else {
		var all []article.ArticleError
		all, err = repository.ListArticlesWithErrors(ctx)
		if err == nil {
			for _, item := range all {
				if item.ExternalID == externalID {
					records = []article.ArticleError{item}
					break
				}
			}
			if len(records) == 0 {
				_, err = repository.GetArticleByExternalID(ctx, externalID)
				if err == nil {
					_, err = fmt.Fprintf(output, "Article external_id=%s has no recorded error.\n", externalID)
					return err
				}
			}
		}
	}
	if err != nil {
		return err
	}
	if len(records) == 0 {
		_, err := fmt.Fprintln(output, "No failed articles found.")
		return err
	}
	writer := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "ARTICLE_ID\tEXTERNAL_ID\tTITLE\tSTATUS\tCURRENT_STEP\tOPERATION\tRETRYABLE\tERROR_TIME\tERROR"); err != nil {
		return err
	}
	for _, record := range records {
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			record.ID, record.ExternalID, oneLine(record.Title), record.Status,
			optionalText(record.CurrentStep), optionalText(record.Operation), optionalBool(record.Retryable),
			optionalTime(record.ErrorTime), oneLine(*record.ErrorMessage),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func optionalBool(value *bool) string {
	if value == nil {
		return "-"
	}
	return fmt.Sprint(*value)
}

func optionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Local().Format("2006-01-02 15:04:05")
}

func optionalText(value *string) string {
	if value == nil {
		return "-"
	}
	return oneLine(*value)
}

func oneLine(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
