package moments

import (
	"context"
	"time"
)

type Clusterer interface {
	Cluster(context.Context, []Candidate) ([][]Candidate, error)
}

type Enricher interface {
	Enrich(context.Context, Moment, []Candidate) (Metadata, error)
}

type Metadata struct {
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Confidence  float64 `json:"confidence"`
}

type Option func(*Service)

func WithClusterer(clusterer Clusterer) Option {
	return func(service *Service) {
		if clusterer != nil {
			service.clusterer = clusterer
		}
	}
}

func WithEnricher(enricher Enricher) Option {
	return func(service *Service) { service.enricher = enricher }
}

type temporalClusterer struct{ gap time.Duration }

func (clusterer temporalClusterer) Cluster(_ context.Context, candidates []Candidate) ([][]Candidate, error) {
	return clusterByTime(candidates, clusterer.gap), nil
}
