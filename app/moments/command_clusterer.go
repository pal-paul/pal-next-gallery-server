package moments

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type CommandClusterer struct {
	executable string
	mediaDir   string
}

func NewCommandClusterer(executable, mediaDir string) *CommandClusterer {
	return &CommandClusterer{executable: strings.TrimSpace(executable), mediaDir: filepath.Clean(mediaDir)}
}

func (clusterer *CommandClusterer) Cluster(ctx context.Context, candidates []Candidate) ([][]Candidate, error) {
	request := append([]Candidate(nil), candidates...)
	for index := range request {
		path, err := safeMediaPath(clusterer.mediaDir, request[index].SourcePath)
		if err != nil {
			return nil, err
		}
		request[index].SourcePath = path
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("encode visual clustering request: %w", err)
	}
	command := exec.CommandContext(ctx, clusterer.executable)
	command.Stdin = bytes.NewReader(payload)
	var output bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &output
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("run visual clustering worker: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var groups [][]Candidate
	if err := json.Unmarshal(output.Bytes(), &groups); err != nil {
		return nil, fmt.Errorf("decode visual clustering response: %w", err)
	}
	return restoreClusterResponse(candidates, groups)
}

func restoreClusterResponse(candidates []Candidate, groups [][]Candidate) ([][]Candidate, error) {
	originals := make(map[string]Candidate, len(candidates))
	for _, candidate := range candidates {
		originals[candidate.ID] = candidate
	}
	seen := make(map[string]struct{}, len(candidates))
	for groupIndex := range groups {
		for candidateIndex := range groups[groupIndex] {
			result := groups[groupIndex][candidateIndex]
			original, ok := originals[result.ID]
			if !ok {
				return nil, fmt.Errorf("visual clustering worker returned unknown media ID %q", result.ID)
			}
			if _, ok := seen[result.ID]; ok {
				return nil, fmt.Errorf("visual clustering worker returned duplicate media ID %q", result.ID)
			}
			seen[result.ID] = struct{}{}
			original.SimilarityScore = result.SimilarityScore
			original.RepresentativeScore = result.RepresentativeScore
			original.Representative = result.Representative
			groups[groupIndex][candidateIndex] = original
		}
	}
	if len(seen) != len(candidates) {
		return nil, fmt.Errorf("visual clustering worker omitted %d media candidates", len(candidates)-len(seen))
	}
	return groups, nil
}

func safeMediaPath(root, relative string) (string, error) {
	if strings.TrimSpace(relative) == "" || filepath.IsAbs(relative) {
		return "", fmt.Errorf("invalid media path")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	cleanRelative, err := filepath.Rel(root, path)
	if err != nil || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("media path escapes configured root")
	}
	return path, nil
}
