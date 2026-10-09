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

type ImageDescriber interface {
	Describe(context.Context, Candidate) (ImageDescription, error)
}

type MetadataSynthesizer interface {
	Synthesize(context.Context, Moment, []ImageDescription) (Metadata, error)
}

type SemanticClusterMerger interface {
	MergeClusters(context.Context, [][]ImageDescription) ([][]int, error)
}

type ImageDescription struct {
	MediaID      string   `json:"mediaId,omitempty"`
	Model        string   `json:"-"`
	Version      string   `json:"-"`
	People       []string `json:"people"`
	Activities   []string `json:"activities"`
	LocationType string   `json:"location_type"`
	Objects      []string `json:"objects"`
	Scene        string   `json:"scene"`
	Weather      string   `json:"weather"`
	Description  string   `json:"description"`
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

func WithImageDescriber(describer ImageDescriber) Option {
	return func(service *Service) { service.describer = describer }
}

func WithMetadataSynthesizer(synthesizer MetadataSynthesizer) Option {
	return func(service *Service) { service.synthesizer = synthesizer }
}

type temporalClusterer struct{ gap time.Duration }

func (clusterer temporalClusterer) Cluster(_ context.Context, candidates []Candidate) ([][]Candidate, error) {
	return clusterByTime(candidates, clusterer.gap), nil
}
