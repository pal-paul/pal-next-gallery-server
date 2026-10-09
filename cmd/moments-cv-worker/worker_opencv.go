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

type imageFeatures struct {
	candidate moments.Candidate
	histogram [48]float64
	hash      uint64
	sharpness float64
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

func cluster(features []imageFeatures) [][]moments.Candidate {
	parent := make([]int, len(features))
	for index := range parent {
		parent[index] = index
	}
	for left := range features {
		for right := left + 1; right < len(features); right++ {
			if similarity(features[left], features[right]) >= similarityThreshold {
				join(parent, left, right)
			}
		}
	}
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

func similarity(left, right imageFeatures) float64 {
	var histogramIntersection float64
	for index := range left.histogram {
		histogramIntersection += math.Min(left.histogram[index], right.histogram[index])
	}
	hashSimilarity := 1 - float64(bits.OnesCount64(left.hash^right.hash))/64
	timeSimilarity := proximity(left.candidate.CapturedAt, right.candidate.CapturedAt, 6*time.Hour)
	locationSimilarity := locationProximity(left.candidate, right.candidate)
	return 0.55*histogramIntersection + 0.20*hashSimilarity + 0.15*timeSimilarity + 0.10*locationSimilarity
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
		candidate.RepresentativeScore = 0.55*centrality + 0.45*sharpness
		group = append(group, candidate)
	}
	representativeCount := representativeTarget(len(group))
	ranked := append([]moments.Candidate(nil), group...)
	sort.Slice(ranked, func(left, right int) bool {
		return ranked[left].RepresentativeScore > ranked[right].RepresentativeScore
	})
	representatives := make(map[string]struct{}, representativeCount)
	for _, candidate := range ranked[:representativeCount] {
		representatives[candidate.ID] = struct{}{}
	}
	for index := range group {
		_, group[index].Representative = representatives[group[index].ID]
	}
	sort.Slice(group, func(left, right int) bool {
		return group[left].CapturedAt.Before(group[right].CapturedAt)
	})
	return group
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
