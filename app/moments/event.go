package moments

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

type eventGroup struct {
	id       string
	momentID string
	photos   []EventPhoto
}

func clusterEvents(photos []EventPhoto) []eventGroup {
	byID := make(map[string]*eventGroup)
	seen := make(map[string]bool)
	for _, photo := range photos {
		if seen[photo.ID] {
			continue
		}
		seen[photo.ID] = true
		groupID := "photo:" + photo.ID
		if photo.MomentID != "" {
			groupID = "moment:" + photo.MomentID
		}
		if byID[groupID] == nil {
			byID[groupID] = &eventGroup{id: groupID, momentID: photo.MomentID}
		}
		byID[groupID].photos = append(byID[groupID].photos, photo)
	}
	groups := make([]eventGroup, 0, len(byID))
	for _, group := range byID {
		sortEventPhotos(group.photos)
		groups = append(groups, *group)
	}
	sort.Slice(groups, func(left, right int) bool { return groups[left].id < groups[right].id })
	for {
		scores := make([][]float64, len(groups))
		best := make([]int, len(groups))
		for index := range groups {
			scores[index] = make([]float64, len(groups))
			best[index] = -1
		}
		for left := range groups {
			for right := left + 1; right < len(groups); right++ {
				if groups[left].momentID != "" && groups[right].momentID != "" {
					continue
				}
				score := eventScore(groups[left], groups[right])
				scores[left][right], scores[right][left] = score, score
			}
			for right, score := range scores[left] {
				if left != right && score >= 0.7 && (best[left] == -1 || score > scores[left][best[left]]) {
					best[left] = right
				}
			}
		}
		mergeLeft, mergeRight, highest := -1, -1, 0.0
		for left, right := range best {
			if right <= left || best[right] != left {
				continue
			}
			joined := joinEventGroups(groups[left], groups[right])
			ambiguous := false
			for _, provisional := range []int{left, right} {
				if groups[provisional].momentID != "" {
					continue
				}
				for alternative := range groups {
					if alternative == left || alternative == right || scores[provisional][alternative] <= 0 ||
						scores[left][right]-scores[provisional][alternative] > 0.05 {
						continue
					}
					if (joined.momentID != "" && groups[alternative].momentID != "") || eventScore(joined, groups[alternative]) < 0.7 {
						ambiguous = true
					}
				}
			}
			if !ambiguous && scores[left][right] > highest {
				mergeLeft, mergeRight, highest = left, right, scores[left][right]
			}
		}
		if mergeLeft == -1 {
			return groups
		}
		groups[mergeLeft] = joinEventGroups(groups[mergeLeft], groups[mergeRight])
		groups = append(groups[:mergeRight], groups[mergeRight+1:]...)
		sort.Slice(groups, func(left, right int) bool { return groups[left].id < groups[right].id })
	}
}

func joinEventGroups(left, right eventGroup) eventGroup {
	joined := eventGroup{id: left.id, momentID: left.momentID, photos: append(append([]EventPhoto(nil), left.photos...), right.photos...)}
	if right.momentID != "" {
		joined.id, joined.momentID = right.id, right.momentID
	}
	sortEventPhotos(joined.photos)
	return joined
}

func sortEventPhotos(photos []EventPhoto) {
	sort.Slice(photos, func(left, right int) bool {
		if photos[left].CapturedAt.Equal(photos[right].CapturedAt) {
			return photos[left].ID < photos[right].ID
		}
		return photos[left].CapturedAt.Before(photos[right].CapturedAt)
	})
}

func eventScore(left, right eventGroup) float64 {
	var earliest, latest time.Time
	for _, group := range []eventGroup{left, right} {
		for _, photo := range group.photos {
			if !photo.ReliableCapturedAt || photo.CapturedAt.IsZero() {
				continue
			}
			if earliest.IsZero() || photo.CapturedAt.Before(earliest) {
				earliest = photo.CapturedAt
			}
			if latest.IsZero() || photo.CapturedAt.After(latest) {
				latest = photo.CapturedAt
			}
		}
	}
	if latest.Sub(earliest) > 48*time.Hour {
		return 0
	}
	nearestTime, nearestGPS := math.Inf(1), math.Inf(1)
	for _, leftPhoto := range left.photos {
		for _, rightPhoto := range right.photos {
			if leftPhoto.ReliableCapturedAt && rightPhoto.ReliableCapturedAt && !leftPhoto.CapturedAt.IsZero() && !rightPhoto.CapturedAt.IsZero() {
				nearestTime = math.Min(nearestTime, leftPhoto.CapturedAt.Sub(rightPhoto.CapturedAt).Abs().Hours())
			}
			if validEventGPS(leftPhoto) && validEventGPS(rightPhoto) {
				nearestGPS = math.Min(nearestGPS, coordinateDistanceKM(*leftPhoto.Latitude, *leftPhoto.Longitude, *rightPhoto.Latitude, *rightPhoto.Longitude))
			}
		}
	}
	if (!math.IsInf(nearestTime, 1) && nearestTime > 12) || (!math.IsInf(nearestGPS, 1) && nearestGPS > 50) {
		return 0
	}
	descriptor, pairs := 0.0, 0
	for _, leftPhoto := range eventRepresentatives(left.photos) {
		for _, rightPhoto := range eventRepresentatives(right.photos) {
			descriptor += descriptionSimilarity(leftPhoto.Description, rightPhoto.Description)
			pairs++
		}
	}
	if pairs == 0 {
		return 0
	}
	descriptor /= float64(pairs)
	if descriptor < 0.2 {
		return 0
	}
	score, weight, corroboration := 0.45*descriptor, 0.45, 0.0
	if !math.IsInf(nearestTime, 1) {
		timeScore := math.Exp(-nearestTime / 6)
		score, weight = score+0.30*timeScore, weight+0.30
		corroboration = math.Max(corroboration, timeScore)
	}
	if !math.IsInf(nearestGPS, 1) {
		gpsScore := math.Exp(-nearestGPS / 10)
		score, weight = score+0.25*gpsScore, weight+0.25
		corroboration = math.Max(corroboration, gpsScore)
	}
	if corroboration < 0.5 {
		return 0
	}
	return score / weight
}

func validEventGPS(photo EventPhoto) bool {
	return photo.Latitude != nil && photo.Longitude != nil && !math.IsNaN(*photo.Latitude) && !math.IsNaN(*photo.Longitude) &&
		*photo.Latitude >= -90 && *photo.Latitude <= 90 && *photo.Longitude >= -180 && *photo.Longitude <= 180
}

func eventRepresentatives(photos []EventPhoto) []EventPhoto {
	if len(photos) <= 20 {
		return photos
	}
	representatives := make([]EventPhoto, 20)
	for index := range representatives {
		representatives[index] = photos[index*(len(photos)-1)/19]
	}
	return representatives
}

func descriptionSimilarity(left, right ImageDescription) float64 {
	if left.EmbeddingModel != "" && left.EmbeddingModel == right.EmbeddingModel {
		if similarity, valid := TextCosine(left.Embedding, right.Embedding); valid {
			return math.Max(0, similarity)
		}
	}
	leftTokens, rightTokens := descriptionTokens(left), descriptionTokens(right)
	intersection := 0
	for token := range leftTokens {
		if rightTokens[token] {
			intersection++
		}
	}
	union := len(leftTokens) + len(rightTokens) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func descriptionTokens(description ImageDescription) map[string]bool {
	text := descriptorText(description)
	tokens := make(map[string]bool)
	for _, token := range strings.FieldsFunc(strings.ToLower(text), func(character rune) bool { return !unicode.IsLetter(character) && !unicode.IsNumber(character) }) {
		tokens[token] = true
	}
	return tokens
}

func descriptorText(description ImageDescription) string {
	return strings.TrimSpace(description.Description + " " + strings.Join(description.Keywords, " ") + " " +
		strings.Join(description.Objects, " ") + " " + strings.Join(description.Activities, " "))
}

func TextCosine(left, right []float64) (float64, bool) {
	if len(left) == 0 || len(left) != len(right) {
		return 0, false
	}
	var dot, leftNorm, rightNorm float64
	for index, value := range left {
		other := right[index]
		if math.IsNaN(value) || math.IsInf(value, 0) || math.IsNaN(other) || math.IsInf(other, 0) {
			return 0, false
		}
		dot += value * other
		leftNorm += value * value
		rightNorm += other * other
	}
	denominator := math.Sqrt(leftNorm) * math.Sqrt(rightNorm)
	if denominator == 0 || math.IsInf(denominator, 0) || math.IsNaN(denominator) {
		return 0, false
	}
	return math.Max(-1, math.Min(1, dot/denominator)), true
}
