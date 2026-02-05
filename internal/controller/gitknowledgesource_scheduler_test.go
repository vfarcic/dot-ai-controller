package controller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduleParser_ParseSchedule(t *testing.T) {
	parser := NewScheduleParser()

	tests := []struct {
		name        string
		schedule    string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid @every interval - 24h",
			schedule:    "@every 24h",
			expectError: false,
		},
		{
			name:        "valid @every interval - 6h",
			schedule:    "@every 6h",
			expectError: false,
		},
		{
			name:        "valid @every interval - 1s",
			schedule:    "@every 1s",
			expectError: false,
		},
		{
			name:        "valid standard cron - daily at 3am",
			schedule:    "0 3 * * *",
			expectError: false,
		},
		{
			name:        "valid @daily descriptor",
			schedule:    "@daily",
			expectError: false,
		},
		{
			name:        "valid @hourly descriptor",
			schedule:    "@hourly",
			expectError: false,
		},
		{
			name:        "empty schedule uses default",
			schedule:    "",
			expectError: false,
		},
		{
			name:        "invalid - garbage string",
			schedule:    "invalid-garbage",
			expectError: true,
			errorMsg:    "invalid schedule",
		},
		{
			name:        "invalid - @every without duration",
			schedule:    "@every",
			expectError: true,
			errorMsg:    "invalid schedule",
		},
		{
			name:        "invalid - @every with bad duration",
			schedule:    "@every notaduration",
			expectError: true,
			errorMsg:    "invalid schedule",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sched, err := parser.ParseSchedule(tt.schedule)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorMsg)
				assert.Nil(t, sched)
			} else {
				require.NoError(t, err)
				assert.NotNil(t, sched)
			}
		})
	}
}

func TestScheduleParser_ValidateSchedule(t *testing.T) {
	parser := NewScheduleParser()

	t.Run("valid schedule", func(t *testing.T) {
		err := parser.ValidateSchedule("@every 24h")
		require.NoError(t, err)
	})

	t.Run("invalid schedule", func(t *testing.T) {
		err := parser.ValidateSchedule("not-a-schedule")
		require.Error(t, err)
	})
}

func TestScheduleParser_NextSyncDuration(t *testing.T) {
	parser := NewScheduleParser()

	t.Run("@every interval calculates correct duration", func(t *testing.T) {
		// Last sync was 1 hour ago, schedule is every 6 hours
		// Next sync should be in ~5 hours
		lastSync := time.Now().Add(-1 * time.Hour)

		duration, nextTime, err := parser.NextSyncDuration("@every 6h", lastSync)

		require.NoError(t, err)
		assert.InDelta(t, 5*time.Hour, duration, float64(10*time.Second))
		assert.True(t, nextTime.After(time.Now()))
	})

	t.Run("@every interval with recent sync", func(t *testing.T) {
		lastSync := time.Now()

		duration, nextTime, err := parser.NextSyncDuration("@every 24h", lastSync)

		require.NoError(t, err)
		assert.InDelta(t, 24*time.Hour, duration, float64(10*time.Second))
		assert.True(t, nextTime.After(time.Now()))
	})

	t.Run("zero lastSync uses current time", func(t *testing.T) {
		duration, nextTime, err := parser.NextSyncDuration("@every 1h", time.Time{})

		require.NoError(t, err)
		assert.InDelta(t, 1*time.Hour, duration, float64(10*time.Second))
		assert.True(t, nextTime.After(time.Now()))
	})

	t.Run("old lastSync results in immediate sync", func(t *testing.T) {
		// Last sync was 10 hours ago, schedule is every 6 hours
		lastSync := time.Now().Add(-10 * time.Hour)

		duration, nextTime, err := parser.NextSyncDuration("@every 6h", lastSync)

		require.NoError(t, err)
		// Duration should be 0 or very small (immediate)
		assert.LessOrEqual(t, duration, 10*time.Second)
		assert.True(t, nextTime.Before(time.Now().Add(10*time.Second)))
	})

	t.Run("empty schedule uses default", func(t *testing.T) {
		lastSync := time.Now()

		duration, nextTime, err := parser.NextSyncDuration("", lastSync)

		require.NoError(t, err)
		assert.InDelta(t, 24*time.Hour, duration, float64(10*time.Second))
		assert.True(t, nextTime.After(time.Now()))
	})

	t.Run("invalid schedule returns error", func(t *testing.T) {
		duration, nextTime, err := parser.NextSyncDuration("invalid-schedule", time.Now())

		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid schedule")
		assert.Equal(t, time.Duration(0), duration)
		assert.True(t, nextTime.IsZero())
	})

	t.Run("standard cron calculates next occurrence", func(t *testing.T) {
		lastSync := time.Now()

		duration, nextTime, err := parser.NextSyncDuration("0 3 * * *", lastSync)

		require.NoError(t, err)
		assert.True(t, duration >= 0)
		assert.True(t, nextTime.After(time.Now()) || nextTime.Equal(time.Now()))
		assert.Equal(t, 3, nextTime.Hour())
		assert.Equal(t, 0, nextTime.Minute())
	})
}

func TestDefaultSchedule(t *testing.T) {
	assert.Equal(t, "@every 24h", DefaultSchedule)
}
