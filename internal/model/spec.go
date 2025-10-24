package model

import "time"

type BackupSpec struct {
	Enabled       bool   `json:"enabled"`
	Type          string `json:"type"`
	Conn          string `json:"conn"`
	Database      string `json:"database"`
	Cron          string `json:"cron"`
	Dest          string `json:"dest"`
	Prefix        string `json:"prefix"`
	Webhook       string `json:"webhook"`
	Retention     time.Duration `json:"retention"`
	ContainerID   string `json:"container_id"`
	ContainerName string `json:"container_name"`
} 