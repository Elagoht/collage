package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// Static builds are where a content site's dynamic routes stop being convenient.
//
// A live server answers "/category/{slug}" for whatever slug arrives. A directory of
// files has to be told which ones exist, so the builder asks a PathProvider — and
// answering that question is the application's job, because only it knows where the
// content comes from. Here it comes from the same API the site renders from.

// buildTimeout bounds the whole build. A site that cannot enumerate itself in a
// minute has a backend problem, and a build that hangs in CI is worse than one that
// fails there.
const buildTimeout = time.Minute

// pathProvider enumerates the concrete URLs the dynamic pages exist at.
//
// One type answers for every page, dispatching on the page's name. The alternative —
// a provider per page — means the builder needs a registry to pick between them, and
// the framework deliberately does not have one: it asks one question and expects one
// answer.
type pathProvider struct {
	client *Client
	log    *slog.Logger
}

// Paths implements collage.PathProvider.
//
// The listings are asked for a page size at the API's ceiling rather than walked
// page by page. That is a real limit and it is stated rather than hidden: a corpus
// larger than the API's maximum page would be built incompletely, and the fix is
// pagination here, not a larger ceiling there.
func (p pathProvider) Paths(ctx context.Context, page *collage.Page, locale string) ([]collage.PathInstance, error) {
	u := urls{Locale: locale}

	switch page.Name {
	case "category":
		categories, err := p.client.Categories(ctx)
		if err != nil {
			return nil, fmt.Errorf("enumerate categories: %w", err)
		}
		instances := make([]collage.PathInstance, 0, len(categories))
		for _, category := range categories {
			instances = append(instances, collage.PathInstance{
				Path:   u.Category(category.Slug),
				Params: map[string]string{"slug": category.Slug},
			})
		}
		return instances, nil

	case "author":
		// The API has no authors listing the client exposes, so the authors are
		// taken from the articles themselves. Deduplicated, because a writer with
		// four pieces would otherwise be built four times — and the second write
		// would be an ErrOutputPathCollision rather than a wasted render.
		listing, err := p.client.Articles(ctx, listQuery{PerPage: buildPageSize})
		if err != nil {
			return nil, fmt.Errorf("enumerate authors: %w", err)
		}
		seen := make(map[string]struct{}, len(listing.Items))
		instances := make([]collage.PathInstance, 0, len(listing.Items))
		for _, article := range listing.Items {
			if _, done := seen[article.Author]; done {
				continue
			}
			seen[article.Author] = struct{}{}
			instances = append(instances, collage.PathInstance{
				Path:   u.Author(article.Author),
				Params: map[string]string{"slug": article.Author},
			})
		}
		return instances, nil

	case "article":
		listing, err := p.client.Articles(ctx, listQuery{PerPage: buildPageSize})
		if err != nil {
			return nil, fmt.Errorf("enumerate articles: %w", err)
		}
		instances := make([]collage.PathInstance, 0, len(listing.Items))
		for _, article := range listing.Items {
			instances = append(instances, collage.PathInstance{
				Path: u.Article(article),
				Params: map[string]string{
					"year":  article.Year(),
					"month": article.Month(),
					"slug":  article.Slug,
				},
			})
		}
		return instances, nil
	}

	// A dynamic page this provider does not know about. Returning nothing rather
	// than an error lets the build report it as skipped, by name, instead of
	// failing the whole run over one page nobody asked for.
	p.log.Warn("build: no paths for a dynamic page", "page", page.Name)
	return nil, nil
}

// buildPageSize is what the enumeration asks for. It is the API's own ceiling; see
// Paths for why that is a limit rather than a number.
const buildPageSize = 24

// runBuild renders the site to outDir and reports what happened.
//
// It returns the number of errors rather than the errors themselves, because the
// report already carries them and printing them twice helps nobody.
func runBuild(cfg config, outDir string) error {
	app, client, err := newSite(cfg)
	if err != nil {
		return fmt.Errorf("build site: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()

	builder, err := collage.NewBuilder(app, collage.BuildOptions{
		OutDir: outDir,
		// Clean, because a build that leaves last run's files behind produces a
		// directory nobody can reason about: a page deleted from the corpus stays
		// on disk and keeps being deployed.
		Clean:        true,
		PathProvider: pathProvider{client: client, log: cfg.Logger},
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}

	report, buildErr := builder.Build(ctx)

	for _, skipped := range report.Skipped {
		cfg.Logger.Info("build: skipped", "page", skipped.Page, "locale", skipped.Locale, "reason", skipped.Reason)
	}
	for _, e := range report.Errors {
		cfg.Logger.Error("build: failed", "err", e)
	}
	cfg.Logger.Info("build: finished",
		"written", len(report.Written),
		"skipped", len(report.Skipped),
		"errors", len(report.Errors),
		"duration", report.Duration,
		"outDir", outDir,
	)
	return buildErr
}
