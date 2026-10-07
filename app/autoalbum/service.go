package autoalbum

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	DailyAlbumThreshold = 15
	MaxAlbumMedia       = 100
)

type Media struct {
	ID           string
	AlbumOwnerID string
	CreatedAt    time.Time
}

type Assignment struct {
	OwnerID        string
	MediaID        string
	DayStart       time.Time
	WeekStart      time.Time
	WeekEnd        time.Time
	DailyAlbumID   string
	DailyKey       string
	DailyTitle     string
	WeeklyAlbumID  string
	WeeklyKey      string
	WeeklyTitle    string
	DailyThreshold int
	MaxAlbumMedia  int
}

type Repository interface {
	ListAutomaticAlbumMedia(context.Context) ([]Media, error)
	AssignAutomaticAlbum(context.Context, Assignment) error
}

type Service struct {
	repository Repository
}

func NewService(repository Repository) *Service {
	return &Service{repository: repository}
}

func (service *Service) Run(ctx context.Context) error {
	media, err := service.repository.ListAutomaticAlbumMedia(ctx)
	if err != nil {
		return err
	}
	reconciledDays := make(map[string]struct{})
	for _, item := range media {
		dayStart, _, _ := albumPeriod(item.CreatedAt)
		key := item.AlbumOwnerID + ":" + dayStart.Format("2006-01-02")
		if _, reconciled := reconciledDays[key]; reconciled {
			continue
		}
		if err := service.repository.AssignAutomaticAlbum(ctx, assignmentFor(item)); err != nil {
			return err
		}
		reconciledDays[key] = struct{}{}
	}
	return nil
}

func assignmentFor(media Media) Assignment {
	dayStart, weekStart, weekEnd := albumPeriod(media.CreatedAt)
	return Assignment{
		OwnerID:        media.AlbumOwnerID,
		MediaID:        media.ID,
		DayStart:       dayStart,
		WeekStart:      weekStart,
		WeekEnd:        weekEnd,
		DailyAlbumID:   uuid.NewString(),
		DailyKey:       "day:" + dayStart.Format("2006-01-02"),
		DailyTitle:     dayStart.Format("January 2"),
		WeeklyAlbumID:  uuid.NewString(),
		WeeklyKey:      "week:" + weekStart.Format("2006-01-02"),
		WeeklyTitle:    weeklyTitle(weekStart, weekEnd),
		DailyThreshold: DailyAlbumThreshold,
		MaxAlbumMedia:  MaxAlbumMedia,
	}
}

type Schedule struct {
	Location *time.Location
	Hour     int
	Minute   int
}

func NewSchedule(runAt, timezone string) (Schedule, error) {
	clock, err := time.Parse("15:04", runAt)
	if err != nil {
		return Schedule{}, fmt.Errorf("parse automatic album run time: %w", err)
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Schedule{}, fmt.Errorf("load automatic album timezone: %w", err)
	}
	return Schedule{Location: location, Hour: clock.Hour(), Minute: clock.Minute()}, nil
}

func (schedule Schedule) NextRun(now time.Time) time.Time {
	now = now.In(schedule.Location)
	next := time.Date(now.Year(), now.Month(), now.Day(), schedule.Hour, schedule.Minute, 0, 0, schedule.Location)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (service *Service) RunScheduled(ctx context.Context, schedule Schedule, onError func(error)) {
	for {
		timer := time.NewTimer(time.Until(schedule.NextRun(time.Now())))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
			if err := service.Run(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
	}
}

func albumPeriod(createdAt time.Time) (time.Time, time.Time, time.Time) {
	createdAt = createdAt.UTC()
	dayStart := time.Date(createdAt.Year(), createdAt.Month(), createdAt.Day(), 0, 0, 0, 0, time.UTC)
	daysSinceMonday := (int(dayStart.Weekday()) + 6) % 7
	weekStart := dayStart.AddDate(0, 0, -daysSinceMonday)
	return dayStart, weekStart, weekStart.AddDate(0, 0, 6)
}

func weeklyTitle(start, end time.Time) string {
	switch {
	case start.Year() != end.Year():
		return fmt.Sprintf("%s - %s", start.Format("January 2, 2006"), end.Format("January 2, 2006"))
	case start.Month() != end.Month():
		return fmt.Sprintf("%s - %s", start.Format("January 2"), end.Format("January 2"))
	default:
		return fmt.Sprintf("%s - %d", start.Format("January 2"), end.Day())
	}
}
