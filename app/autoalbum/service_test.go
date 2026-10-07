package autoalbum

import (
	"context"
	"testing"
	"time"
)

type recordingRepository struct {
	media       []Media
	assignments []Assignment
}

func (repository *recordingRepository) ListAutomaticAlbumMedia(context.Context) ([]Media, error) {
	return repository.media, nil
}

func (repository *recordingRepository) AssignAutomaticAlbum(_ context.Context, assignment Assignment) error {
	repository.assignments = append(repository.assignments, assignment)
	return nil
}

func TestRunAssignsOctoberWeek(t *testing.T) {
	repository := &recordingRepository{media: []Media{{
		ID: "media-1", AlbumOwnerID: "user-1",
		CreatedAt: time.Date(2026, time.October, 8, 14, 30, 0, 0, time.FixedZone("CEST", 2*60*60)),
	}}}
	service := NewService(repository)

	if err := service.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(repository.assignments) != 1 {
		t.Fatalf("expected one assignment, got %d", len(repository.assignments))
	}
	assignment := repository.assignments[0]
	if assignment.WeeklyTitle != "October 5 - 11" || assignment.WeeklyKey != "week:2026-10-05" {
		t.Fatalf("unexpected weekly album: %#v", assignment)
	}
	if assignment.DailyTitle != "October 8" || assignment.DailyKey != "day:2026-10-08" {
		t.Fatalf("unexpected daily album: %#v", assignment)
	}
	if assignment.DailyThreshold != 15 || assignment.MaxAlbumMedia != 100 || assignment.MediaID != "media-1" {
		t.Fatalf("unexpected assignment: %#v", assignment)
	}
	if assignment.OwnerID != "user-1" {
		t.Fatalf("automatic album owner = %q, want user-1", assignment.OwnerID)
	}
}

func TestScheduleNextRun(t *testing.T) {
	schedule, err := NewSchedule("02:30", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 8, 3, 0, 0, 0, time.UTC)
	want := time.Date(2026, time.October, 9, 2, 30, 0, 0, time.UTC)
	if got := schedule.NextRun(now); !got.Equal(want) {
		t.Fatalf("next run = %s, want %s", got, want)
	}
}

func TestRunAssignsSharedMediaToEachUsersAutomaticAlbum(t *testing.T) {
	createdAt := time.Date(2026, time.October, 8, 14, 30, 0, 0, time.UTC)
	repository := &recordingRepository{media: []Media{
		{ID: "media-1", AlbumOwnerID: "owner-a", CreatedAt: createdAt},
		{ID: "media-1", AlbumOwnerID: "recipient-b", CreatedAt: createdAt},
	}}
	if err := NewService(repository).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.assignments) != 2 {
		t.Fatalf("expected owner and recipient assignments, got %d", len(repository.assignments))
	}
	if repository.assignments[0].OwnerID != "owner-a" || repository.assignments[1].OwnerID != "recipient-b" {
		t.Fatalf("unexpected automatic album owners: %#v", repository.assignments)
	}
}

func TestRunReconcilesOwnerDayOnce(t *testing.T) {
	createdAt := time.Date(2026, time.October, 8, 14, 30, 0, 0, time.UTC)
	repository := &recordingRepository{media: []Media{
		{ID: "media-1", AlbumOwnerID: "owner-a", CreatedAt: createdAt},
		{ID: "media-2", AlbumOwnerID: "owner-a", CreatedAt: createdAt.Add(time.Hour)},
	}}
	if err := NewService(repository).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(repository.assignments) != 1 {
		t.Fatalf("expected one reconciliation for the owner and day, got %d", len(repository.assignments))
	}
}

func TestWeeklyTitleAcrossYear(t *testing.T) {
	start := time.Date(2027, time.December, 27, 0, 0, 0, 0, time.UTC)
	if title := weeklyTitle(start, start.AddDate(0, 0, 6)); title != "December 27, 2027 - January 2, 2028" {
		t.Fatalf("unexpected title %q", title)
	}
}
