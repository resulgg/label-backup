package model

import "time"

// BackupSpec holds the backup configuration for a container, parsed from Docker labels.
type BackupSpec struct {
	Enabled       bool   `json:"enabled"`
	Type          string `json:"type"`      // postgres|mysql|mongodb|redis
	Conn          string `json:"conn"`      // Connection string or details
	Database      string `json:"database"`  // Specific database to back up
	Cron          string `json:"cron"`      // Cron schedule
	Dest          string `json:"dest"`      // local|remote
	Prefix        string `json:"prefix"`    // Object key prefix for remote storage
	Webhook       string `json:"webhook"`   // Optional URL override for notifications
	Retention     time.Duration `json:"retention"` // Optional retention days override
	ContainerID   string `json:"container_id"` // Added for convenience
	ContainerName string `json:"container_name"` // Added for webhook payload
} 