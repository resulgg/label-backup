package discovery

import (
	"testing"
	"time"
)

func TestParseRetentionDuration(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected time.Duration
	}{
		{
			name:     "empty string",
			input:    "",
			expected: 0,
		},
		{
			name:     "7 days",
			input:    "7d",
			expected: 7 * 24 * time.Hour,
		},
		{
			name:     "24 hours",
			input:    "24h",
			expected: 24 * time.Hour,
		},
		{
			name:     "30 minutes",
			input:    "30m",
			expected: 30 * time.Minute,
		},
		{
			name:     "plain number (days)",
			input:    "10",
			expected: 10 * 24 * time.Hour,
		},
		{
			name:     "negative days",
			input:    "-5d",
			expected: 0,
		},
		{
			name:     "invalid format",
			input:    "invalid",
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseRetentionDuration(tt.input, "test-container")
			if result != tt.expected {
				t.Errorf("parseRetentionDuration(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseLabels(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected bool
	}{
		{
			name: "valid backup configuration",
			labels: map[string]string{
				"backup.enabled": "true",
				"backup.cron":    "0 2 * * *",
				"backup.type":    "postgres",
				"backup.conn":    "postgresql://user:pass@host:5432/db",
			},
			expected: true,
		},
		{
			name: "backup disabled",
			labels: map[string]string{
				"backup.enabled": "false",
				"backup.cron":    "0 2 * * *",
				"backup.type":    "postgres",
			},
			expected: false,
		},
		{
			name: "missing cron",
			labels: map[string]string{
				"backup.enabled": "true",
				"backup.type":    "postgres",
				"backup.conn":    "postgresql://user:pass@host:5432/db",
			},
			expected: false,
		},
		{
			name: "missing type",
			labels: map[string]string{
				"backup.enabled": "true",
				"backup.cron":    "0 2 * * *",
				"backup.conn":    "postgresql://user:pass@host:5432/db",
			},
			expected: false,
		},
		{
			name: "redis without conn",
			labels: map[string]string{
				"backup.enabled": "true",
				"backup.cron":    "0 2 * * *",
				"backup.type":    "redis",
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := parseLabels(tt.labels, "test-container", "test-container")
			if ok != tt.expected {
				t.Errorf("parseLabels() = %v, want %v", ok, tt.expected)
			}
			if tt.expected && !spec.Enabled {
				t.Errorf("expected enabled spec, got disabled")
			}
		})
	}
}
