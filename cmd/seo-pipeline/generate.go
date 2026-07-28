package main

import (
	"context"

	"github.com/foxylis237/seo-pipeline/internal/generation"
)

func runGenerate(ctx context.Context, pipeline *generation.Pipeline, externalID string) error {
	_, err := pipeline.RunByExternalID(ctx, externalID)
	return err
}
