package documents

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

func HealthDocument() *collage.Document {
	return collage.NewDocument("health", "application/json").
		WithPath("en", "/healthz").
		WithHandler(sayOk).
		Dynamic().
		Build()
}

type healthResponse struct {
	Status string    `json:"status"`
	Date   time.Time `json:"date"`
}

func sayOk(context.Context, *collage.RenderContext) ([]byte, []string, error) {
	body, err := json.MarshalIndent(healthResponse{
		Status: "ok",
		Date:   time.Now().UTC(),
	}, "", "\t")
	if err != nil {
		return nil, nil, fmt.Errorf("health: marshal: %w", err)
	}
	return body, nil, nil
}
