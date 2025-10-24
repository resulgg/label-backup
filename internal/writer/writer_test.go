package writer

import (
	"context"
	"strings"
	"testing"

	"label-backup/internal/model"
)

func TestGenerateObjectName(t *testing.T) {
	tests := []struct {
		name     string
		spec     model.BackupSpec
		expected string
	}{
		{
			name: "basic postgres backup",
			spec: model.BackupSpec{
				Type:     "postgres",
				Database: "mydb",
				Prefix:   "",
			},
			expected: "postgres-mydb-",
		},
		{
			name: "with prefix",
			spec: model.BackupSpec{
				Type:     "mysql",
				Database: "testdb",
				Prefix:   "backups/prod",
			},
			expected: "backups/prod/mysql-testdb-",
		},
		{
			name: "redis without database",
			spec: model.BackupSpec{
				Type:     "redis",
				Database: "",
				Prefix:   "",
			},
			expected: "redis-default-",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GenerateObjectName(tt.spec)
			if len(result) < len(tt.expected) {
				t.Errorf("GenerateObjectName() = %v, too short", result)
			}
			if result[:len(tt.expected)] != tt.expected {
				t.Errorf("GenerateObjectName() = %v, want prefix %v", result, tt.expected)
			}
			if len(result) < 8 || result[len(result)-8:] != ".dump.gz" {
				t.Errorf("GenerateObjectName() = %v, should end with .dump.gz", result)
			}
		})
	}
}

func TestValidateBackup(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		expectError bool
	}{
		{
			name:        "valid gzip header",
			data:        []byte{0x1f, 0x8b, 0x08, 0x00},
			expectError: false,
		},
		{
			name:        "invalid header",
			data:        []byte{0x00, 0x00, 0x00, 0x00},
			expectError: true,
		},
		{
			name:        "empty data",
			data:        []byte{},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := strings.NewReader(string(tt.data))
			_, err := ValidateBackup(context.Background(), reader)
			if (err != nil) != tt.expectError {
				t.Errorf("ValidateBackup() error = %v, wantErr %v", err, tt.expectError)
			}
		})
	}
}
