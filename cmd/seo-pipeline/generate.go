package main

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/generation"
)

func runGenerate(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunByExternalID(ctx, externalID)
	return err
}

func runArticle(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunArticleByExternalID(ctx, externalID)
	return err
}

func runInfo(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunInfoByExternalID(ctx, externalID)
	return err
}

func runReview(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunReviewByExternalID(ctx, externalID)
	return err
}

func runFix(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunFixByExternalID(ctx, externalID)
	return err
}

func runHTML(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunHTMLByExternalID(ctx, externalID)
	return err
}
