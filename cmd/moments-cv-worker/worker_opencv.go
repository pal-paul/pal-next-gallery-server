//go:build opencv

package main

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"math/bits"
	"os"
	"sort"
	"time"

	"pal-next-gallery-server/app/moments"

	"gocv.io/x/gocv"
)

const similarityThreshold = 0.62

type weightedEdge struct {
	left   int
	right  int
	weight float64
}

type imageFeatures struct {
	candidate moments.Candidate
	histogram [48]float64
	hash      uint64
	sharpness float64
	exposure  float64
}

func run() error {
	var candidates []moments.Candidate
	if err := json.NewDecoder(os.Stdin).Decode(&candidates); err != nil {
		return fmt.Errorf("decode candidates: %w", err)
	}
	features := make([]imageFeatures, 0, len(candidates))
	for _, candidate := range candidates {
		feature, err := extractFeatures(candidate)
		if err != nil {
			return err
		}
		features = append(features, feature)
	}
	return json.NewEncoder(os.Stdout).Encode(cluster(features))
}

func extractFeatures(candidate moments.Candidate) (imageFeatures, error) {
	source := gocv.IMRead(candidate.SourcePath, gocv.IMReadColor)
	if source.Empty() {
		source.Close()
		return imageFeatures{}, fmt.Errorf("open image %s", candidate.ID)
	}
	defer source.Close()
	color := gocv.NewMat()
	defer color.Close()
	gocv.Resize(source, &color, image.Pt(64, 64), 0, 0, gocv.InterpolationArea)
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(color, &gray, gocv.ColorBGRToGray)
	hashImage := gocv.NewMat()
	defer hashImage.Close()
	gocv.Resize(gray, &hashImage, image.Pt(9, 8), 0, 0, gocv.InterpolationArea)
	return imageFeatures{
		candidate: candidate,
		histogram: colorHistogram(color.ToBytes()),
		hash:      differenceHash(hashImage.ToBytes()),
		sharpness: edgeSharpness(gray.ToBytes(), gray.Cols(), gray.Rows()),
		exposure:  exposureQuality(gray.ToBytes()),
	}, nil
}

func colorHistogram(pixels []byte) [48]float64 {
	var histogram [48]float64
	for offset := 0; offset+2 < len(pixels); offset += 3 {
		for channel := 0; channel < 3; channel++ {
			histogram[channel*16+int(pixels[offset+channel])/16]++
		}
	}
	total := float64(len(pixels))
	if total > 0 {
		for index := range histogram {
			histogram[index] = histogram[index] * 3 / total
		}
	}
	return histogram
}

func differenceHash(pixels []byte) uint64 {
	var hash uint64
	for row := 0; row < 8; row++ {
		for column := 0; column < 8; column++ {
			if pixels[row*9+column] > pixels[row*9+column+1] {
				hash |= 1 << uint(row*8+column)
			}
		}
	}
	return hash
}

func edgeSharpness(pixels []byte, width, height int) float64 {
	if width < 2 || height < 2 {
		return 0
	}
	var total float64
	for row := 0; row < height-1; row++ {
		for column := 0; column < width-1; column++ {
			index := row*width + column
			total += math.Abs(float64(pixels[index])-float64(pixels[index+1])) +
				math.Abs(float64(pixels[index])-float64(pixels[index+width]))
		}
	}
	return total / float64((width-1)*(height-1)*2*255)
}

func exposureQuality(pixels []byte) float64 {
	if len(pixels) == 0 {
		return 0
	}
	var total float64
	clipped := 0
	for _, pixel := range pixels {
		total += float64(pixel) / 255
		if pixel <= 5 || pixel >= 250 {
			clipped++
		}
	}
	mean := total / float64(len(pixels))
	balanced := 1 - math.Min(1, math.Abs(mean-0.5)*2)
	return balanced * (1 - float64(clipped)/float64(len(pixels)))
}

func cluster(features []imageFeatures) [][]moments.Candidate {
	if len(features) == 0 {
		return nil
	}
	parent := densityComponents(features)
	components := make(map[int][]int)
	for index := range features {
		root := find(parent, index)
		components[root] = append(components[root], index)
	}
	groups := make([][]moments.Candidate, 0, len(components))
	for _, indexes := range components {
		groups = append(groups, scoreRepresentatives(features, indexes))
	}
	sort.Slice(groups, func(left, right int) bool {
		return groups[left][0].CapturedAt.Before(groups[right][0].CapturedAt)
	})
	return groups
}

func densityComponents(features []imageFeatures) []int {
	distances := make([][]float64, len(features))
	coreDistances := make([]float64, len(features))
	for left := range features {
		distances[left] = make([]float64, len(features))
		neighbors := make([]float64, 0, len(features)-1)
		for right := range features {
			if left == right {
				continue
			}
			distance := 1 - similarity(features[left], features[right])
			distances[left][right] = distance
			neighbors = append(neighbors, distance)
		}
		sort.Float64s(neighbors)
		if len(neighbors) > 0 {
			coreDistances[left] = neighbors[min(1, len(neighbors)-1)]
		}
	}
	edges := make([]weightedEdge, 0, len(features)*(len(features)-1)/2)
	for left := range features {
		for right := left + 1; right < len(features); right++ {
			edges = append(edges, weightedEdge{left: left, right: right,
				weight: max(distances[left][right], max(coreDistances[left], coreDistances[right]))})
		}
	}
	sort.Slice(edges, func(left, right int) bool { return edges[left].weight < edges[right].weight })
	treeParent := newParents(len(features))
	minimumSpanningTree := make([]weightedEdge, 0, max(0, len(features)-1))
	for _, edge := range edges {
		if find(treeParent, edge.left) == find(treeParent, edge.right) {
			continue
		}
		join(treeParent, edge.left, edge.right)
		minimumSpanningTree = append(minimumSpanningTree, edge)
	}
	parent := make([]int, len(features))
	for index := range parent {
		parent[index] = index
	}
	for _, edge := range minimumSpanningTree {
		if edge.weight <= 1-similarityThreshold {
			join(parent, edge.left, edge.right)
		}
	}
	return parent
}

func newParents(size int) []int {
	parents := make([]int, size)
	for index := range parents {
		parents[index] = index
	}
	return parents
}

func similarity(left, right imageFeatures) float64 {
	var histogramIntersection float64
	for index := range left.histogram {
		histogramIntersection += math.Min(left.histogram[index], right.histogram[index])
	}
	hashSimilarity := 1 - float64(bits.OnesCount64(left.hash^right.hash))/64
	visualSimilarity := histogramIntersection
	if embeddingSimilarity, ok := cosineSimilarity(left.candidate.Embedding, right.candidate.Embedding); ok {
		visualSimilarity = embeddingSimilarity
	}
	timeSimilarity := proximity(left.candidate.CapturedAt, right.candidate.CapturedAt, 6*time.Hour)
	locationSimilarity := locationProximity(left.candidate, right.candidate)
	return 0.55*visualSimilarity + 0.20*hashSimilarity + 0.15*timeSimilarity + 0.10*locationSimilarity
}

func cosineSimilarity(left, right []float64) (float64, bool) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, false
	}
	var dot, leftMagnitude, rightMagnitude float64
	for index := range left {
		dot += left[index] * right[index]
		leftMagnitude += left[index] * left[index]
		rightMagnitude += right[index] * right[index]
	}
	if leftMagnitude == 0 || rightMagnitude == 0 {
		return 0, false
	}
	return (dot/math.Sqrt(leftMagnitude*rightMagnitude) + 1) / 2, true
}

func proximity(left, right time.Time, limit time.Duration) float64 {
	difference := left.Sub(right).Abs()
	if difference >= limit {
		return 0
	}
	return 1 - float64(difference)/float64(limit)
}

func locationProximity(left, right moments.Candidate) float64 {
	if left.Latitude == nil || left.Longitude == nil || right.Latitude == nil || right.Longitude == nil {
		return 0.5
	}
	distance := haversine(*left.Latitude, *left.Longitude, *right.Latitude, *right.Longitude)
	if distance >= 50 {
		return 0
	}
	return 1 - distance/50
}

func haversine(latitude1, longitude1, latitude2, longitude2 float64) float64 {
	const earthRadiusKM = 6371.0
	latitudeDelta := (latitude2 - latitude1) * math.Pi / 180
	longitudeDelta := (longitude2 - longitude1) * math.Pi / 180
	leftLatitude := latitude1 * math.Pi / 180
	rightLatitude := latitude2 * math.Pi / 180
	a := math.Sin(latitudeDelta/2)*math.Sin(latitudeDelta/2) +
		math.Cos(leftLatitude)*math.Cos(rightLatitude)*math.Sin(longitudeDelta/2)*math.Sin(longitudeDelta/2)
	return earthRadiusKM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func scoreRepresentatives(features []imageFeatures, indexes []int) []moments.Candidate {
	maxSharpness := 0.0
	for _, index := range indexes {
		maxSharpness = math.Max(maxSharpness, features[index].sharpness)
	}
	group := make([]moments.Candidate, 0, len(indexes))
	for _, index := range indexes {
		centrality := 1.0
		if len(indexes) > 1 {
			centrality = 0
			for _, other := range indexes {
				if index != other {
					centrality += similarity(features[index], features[other])
				}
			}
			centrality /= float64(len(indexes) - 1)
		}
		sharpness := 0.0
		if maxSharpness > 0 {
			sharpness = features[index].sharpness / maxSharpness
		}
		candidate := features[index].candidate
		candidate.SimilarityScore = centrality
		candidate.RepresentativeScore = 0.45*centrality + 0.35*sharpness + 0.20*features[index].exposure
		group = append(group, candidate)
	}
	representativeCount := representativeTarget(len(group))
	representatives := selectDiverseRepresentatives(features, indexes, group, representativeCount)
	for index := range group {
		_, group[index].Representative = representatives[group[index].ID]
	}
	sort.Slice(group, func(left, right int) bool {
		return group[left].CapturedAt.Before(group[right].CapturedAt)
	})
	return group
}

func selectDiverseRepresentatives(features []imageFeatures, indexes []int, group []moments.Candidate, count int) map[string]struct{} {
	selected := make(map[string]struct{}, count)
	selectedIndexes := make([]int, 0, count)
	for len(selected) < count {
		bestGroupIndex := -1
		bestSelectionScore := -1.0
		for groupIndex, candidate := range group {
			if _, exists := selected[candidate.ID]; exists {
				continue
			}
			diversity := 1.0
			for _, selectedIndex := range selectedIndexes {
				diversity = math.Min(diversity, 1-similarity(features[indexes[groupIndex]], features[selectedIndex]))
			}
			selectionScore := 0.65*candidate.RepresentativeScore + 0.35*diversity
			if selectionScore > bestSelectionScore ||
				(selectionScore == bestSelectionScore && (bestGroupIndex < 0 || candidate.ID < group[bestGroupIndex].ID)) {
				bestGroupIndex = groupIndex
				bestSelectionScore = selectionScore
			}
		}
		if bestGroupIndex < 0 {
			break
		}
		selected[group[bestGroupIndex].ID] = struct{}{}
		selectedIndexes = append(selectedIndexes, indexes[bestGroupIndex])
	}
	return selected
}

func representativeTarget(groupSize int) int {
	if groupSize == 0 {
		return 0
	}
	if groupSize < 5 {
		return 1
	}
	target := int(math.Ceil(math.Sqrt(float64(groupSize))))
	return min(max(target, 5), min(groupSize, 15))
}

func find(parent []int, index int) int {
	if parent[index] != index {
		parent[index] = find(parent, parent[index])
	}
	return parent[index]
}

func join(parent []int, left, right int) {
	leftRoot := find(parent, left)
	rightRoot := find(parent, right)
	if leftRoot != rightRoot {
		parent[rightRoot] = leftRoot
	}
}
