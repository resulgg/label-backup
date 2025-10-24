package gc

import (
	"context"
	"io"
	"testing"
	"time"

	"label-backup/internal/model"
	"label-backup/internal/writer"
)

type mockBackupWriter struct {
	objects []writer.BackupObjectMeta
}

func (m *mockBackupWriter) Type() string {
	return "mock"
}

func (m *mockBackupWriter) Write(ctx context.Context, objectName string, reader io.Reader) (string, int64, string, error) {
	return "", 0, "", nil
}

func (m *mockBackupWriter) ReadObject(ctx context.Context, objectName string) (io.ReadCloser, error) {
	return io.NopCloser(io.Reader(nil)), nil
}

func (m *mockBackupWriter) ListObjects(ctx context.Context, prefix string) ([]writer.BackupObjectMeta, error) {
	return m.objects, nil
}

func (m *mockBackupWriter) DeleteObject(ctx context.Context, key string) error {
	for i, obj := range m.objects {
		if obj.Key == key {
			m.objects = append(m.objects[:i], m.objects[i+1:]...)
			break
		}
	}
	return nil
}

func TestRunGC(t *testing.T) {
	now := time.Now().UTC()
	oldTime := now.Add(-10 * 24 * time.Hour)
	recentTime := now.Add(-1 * 24 * time.Hour) 

	tests := []struct {
		name           string
		objects        []writer.BackupObjectMeta
		retention      time.Duration
		expectedDeletes int
		dryRun         bool
	}{
		{
			name: "delete old objects",
			objects: []writer.BackupObjectMeta{
				{Key: "old1.dump.gz", LastModified: oldTime},
				{Key: "old2.dump.gz", LastModified: oldTime},
				{Key: "recent.dump.gz", LastModified: recentTime},
			},
			retention:      7 * 24 * time.Hour,
			expectedDeletes: 2,
			dryRun:         false,
		},
		{
			name: "dry run mode",
			objects: []writer.BackupObjectMeta{
				{Key: "old1.dump.gz", LastModified: oldTime},
			},
			retention:      7 * 24 * time.Hour,
			expectedDeletes: 1,
			dryRun:         true,
		},
		{
			name: "no objects to delete",
			objects: []writer.BackupObjectMeta{
				{Key: "recent1.dump.gz", LastModified: recentTime},
				{Key: "recent2.dump.gz", LastModified: recentTime},
			},
			retention:      7 * 24 * time.Hour,
			expectedDeletes: 0,
			dryRun:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockWriter := &mockBackupWriter{objects: tt.objects}
			originalCount := len(mockWriter.objects)

			spec := model.BackupSpec{
				ContainerID: "test-container",
				Prefix:      "test-prefix",
			}

			runner, err := NewRunner(spec, mockWriter, tt.retention, tt.dryRun)
			if err != nil {
				t.Fatalf("NewRunner() error = %v", err)
			}

			err = runner.RunGC(context.Background())
			if err != nil {
				t.Fatalf("RunGC() error = %v", err)
			}

			finalCount := len(mockWriter.objects)
			deletedCount := originalCount - finalCount

			if deletedCount != tt.expectedDeletes {
				t.Errorf("RunGC() deleted %d objects, want %d", deletedCount, tt.expectedDeletes)
			}
		})
	}
}
