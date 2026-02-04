package controller

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	// DefaultSchedule is used when no schedule is specified
	DefaultSchedule = "@every 24h"
)

// ScheduleParser parses cron and interval expressions for GitKnowledgeSource scheduling.
type ScheduleParser struct {
	parser cron.Parser
}

// NewScheduleParser creates a new ScheduleParser with support for
// standard cron expressions and @every intervals.
func NewScheduleParser() *ScheduleParser {
	// Use a parser that supports:
	// - Standard cron (minute, hour, day, month, weekday): "0 3 * * *"
	// - Descriptors: @yearly, @monthly, @weekly, @daily, @hourly
	// - Intervals: @every <duration>
	return &ScheduleParser{
		parser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor),
	}
}

// ParseSchedule parses a schedule expression and returns a cron.Schedule.
// Supports standard cron expressions (e.g., "0 3 * * *") and intervals (e.g., "@every 24h").
// Returns an error if the schedule is invalid.
func (p *ScheduleParser) ParseSchedule(schedule string) (cron.Schedule, error) {
	if schedule == "" {
		schedule = DefaultSchedule
	}

	sched, err := p.parser.Parse(schedule)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule %q: %w", schedule, err)
	}

	return sched, nil
}

// NextSyncDuration calculates the duration until the next sync based on the schedule
// and the last sync time. Returns:
// - duration: time until next sync (for RequeueAfter)
// - nextTime: the absolute time of the next sync (for status.NextScheduledSync)
// - error: if schedule parsing fails
//
// If lastSync is zero (first sync), uses current time as the reference.
func (p *ScheduleParser) NextSyncDuration(schedule string, lastSync time.Time) (time.Duration, time.Time, error) {
	sched, err := p.ParseSchedule(schedule)
	if err != nil {
		return 0, time.Time{}, err
	}

	// Use lastSync as reference point, or now if not set
	reference := lastSync
	if reference.IsZero() {
		reference = time.Now()
	}

	// Calculate next sync time
	nextTime := sched.Next(reference)

	// Calculate duration from now to next sync
	now := time.Now()
	duration := nextTime.Sub(now)

	// If duration is negative (next time is in the past), sync immediately
	// This can happen if lastSync is very old or schedule parsing edge cases
	if duration < 0 {
		duration = 0
		nextTime = now
	}

	return duration, nextTime, nil
}

// ValidateSchedule checks if a schedule expression is valid without calculating next time.
// Returns nil if valid, error with details if invalid.
func (p *ScheduleParser) ValidateSchedule(schedule string) error {
	_, err := p.ParseSchedule(schedule)
	return err
}
