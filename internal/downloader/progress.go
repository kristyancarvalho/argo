package downloader

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

const (
	progressCheckpointBytes    = 1 << 20
	progressCheckpointInterval = time.Second
	progressFlushTimeout       = 5 * time.Second
)

type progressCheckpoint struct {
	bytes int64
	at    time.Time
}

func (checkpoint progressCheckpoint) due(count int64, now time.Time, force bool) bool {
	return count != checkpoint.bytes && (force || checkpoint.bytes < copyBufferSize || checkpoint.at.IsZero() ||
		count-checkpoint.bytes >= progressCheckpointBytes || now.Sub(checkpoint.at) >= progressCheckpointInterval)
}

func (engine *Engine) persistProgress(
	ctx context.Context,
	id model.DownloadID,
	file *os.File,
	checkpoint *progressCheckpoint,
	count int64,
	force bool,
) error {
	now := engine.now()
	if !checkpoint.due(count, now, force) {
		return nil
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync progress checkpoint: %w", err)
	}
	if err := engine.store.UpdateDownloadProgress(ctx, id, count, now); err != nil {
		return err
	}
	*checkpoint = progressCheckpoint{bytes: count, at: now}
	return nil
}

func (tracker *progressTracker) persistChunk(ctx context.Context, index int, force bool) error {
	now := tracker.engine.now()
	count := tracker.byChunk[index]
	if !tracker.checkpoints[index].due(count, now, force) {
		return nil
	}
	if err := tracker.file.Sync(); err != nil {
		return fmt.Errorf("sync chunk checkpoint: %w", err)
	}
	if err := tracker.engine.store.UpdateChunkProgress(ctx, tracker.downloadID, index, count, now); err != nil {
		return err
	}
	tracker.checkpoints[index] = progressCheckpoint{bytes: count, at: now}
	return nil
}

func (tracker *progressTracker) Flush(ctx context.Context) error {
	tracker.mutex.Lock()
	defer tracker.mutex.Unlock()
	for index := range tracker.byChunk {
		if err := tracker.persistChunk(ctx, index, true); err != nil {
			return err
		}
	}
	return nil
}
