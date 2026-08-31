package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

func (store *Store) ReplaceDownloadChunks(
	ctx context.Context,
	downloadID model.DownloadID,
	chunks []model.DownloadChunk,
	updatedAt time.Time,
) error {
	if err := validateChunks(downloadID, chunks); err != nil {
		return err
	}
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chunk replacement for download %s: %w", downloadID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	if _, err := transaction.ExecContext(
		ctx,
		"DELETE FROM download_chunks WHERE download_id = ?",
		downloadID.String(),
	); err != nil {
		return fmt.Errorf("clear chunks for download %s: %w", downloadID, err)
	}
	var totalDownloaded int64
	for _, chunk := range chunks {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO download_chunks (
            download_id, chunk_index, start_byte, end_byte, downloaded_bytes
        ) VALUES (?, ?, ?, ?, ?)`,
			downloadID.String(),
			chunk.Index,
			chunk.Start,
			chunk.End,
			chunk.DownloadedBytes,
		); err != nil {
			return fmt.Errorf("insert chunk %d for download %s: %w", chunk.Index, downloadID, err)
		}
		totalDownloaded += chunk.DownloadedBytes
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE downloads
        SET downloaded_bytes = ?, updated_at = ? WHERE id = ?`,
		totalDownloaded,
		formatTime(updatedAt),
		downloadID.String(),
	); err != nil {
		return fmt.Errorf("update aggregate chunk progress for download %s: %w", downloadID, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit chunk replacement for download %s: %w", downloadID, err)
	}
	committed = true

	return nil
}

func (store *Store) DownloadChunks(
	ctx context.Context,
	downloadID model.DownloadID,
) ([]model.DownloadChunk, error) {
	rows, err := store.database.QueryContext(ctx, `SELECT
        chunk_index, start_byte, end_byte, downloaded_bytes
        FROM download_chunks WHERE download_id = ? ORDER BY chunk_index`,
		downloadID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("read chunks for download %s: %w", downloadID, err)
	}
	chunks := make([]model.DownloadChunk, 0)
	for rows.Next() {
		chunk := model.DownloadChunk{DownloadID: downloadID}
		if err := rows.Scan(&chunk.Index, &chunk.Start, &chunk.End, &chunk.DownloadedBytes); err != nil {
			return nil, errors.Join(fmt.Errorf("scan chunk for download %s: %w", downloadID, err), rows.Close())
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("iterate chunks for download %s: %w", downloadID, err), rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close chunks for download %s: %w", downloadID, err)
	}

	return chunks, nil
}

func (store *Store) UpdateChunkProgress(
	ctx context.Context,
	downloadID model.DownloadID,
	chunkIndex int,
	downloadedBytes int64,
	updatedAt time.Time,
) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin chunk progress update for download %s: %w", downloadID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	result, err := transaction.ExecContext(ctx, `UPDATE download_chunks
        SET downloaded_bytes = ?
        WHERE download_id = ? AND chunk_index = ?
          AND ? >= 0 AND ? <= end_byte - start_byte + 1`,
		downloadedBytes,
		downloadID.String(),
		chunkIndex,
		downloadedBytes,
		downloadedBytes,
	)
	if err != nil {
		return fmt.Errorf("update chunk %d for download %s: %w", chunkIndex, downloadID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read chunk update result for download %s: %w", downloadID, err)
	}
	if updated != 1 {
		return fmt.Errorf("chunk %d for download %s was not found or progress is invalid", chunkIndex, downloadID)
	}
	var aggregate int64
	if err := transaction.QueryRowContext(ctx, `SELECT COALESCE(SUM(downloaded_bytes), 0)
        FROM download_chunks WHERE download_id = ?`, downloadID.String()).Scan(&aggregate); err != nil {
		return fmt.Errorf("sum chunk progress for download %s: %w", downloadID, err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE downloads
        SET downloaded_bytes = ?, updated_at = ? WHERE id = ?`,
		aggregate,
		formatTime(updatedAt),
		downloadID.String(),
	); err != nil {
		return fmt.Errorf("persist aggregate progress for download %s: %w", downloadID, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit chunk progress for download %s: %w", downloadID, err)
	}
	committed = true

	return nil
}

func (store *Store) ResetDownloadProgress(
	ctx context.Context,
	downloadID model.DownloadID,
	updatedAt time.Time,
) error {
	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin progress reset for download %s: %w", downloadID, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if _, err := transaction.ExecContext(
		ctx,
		"DELETE FROM download_chunks WHERE download_id = ?",
		downloadID.String(),
	); err != nil {
		return fmt.Errorf("clear chunk progress for download %s: %w", downloadID, err)
	}
	result, err := transaction.ExecContext(ctx, `UPDATE downloads
        SET downloaded_bytes = 0, updated_at = ? WHERE id = ?`,
		formatTime(updatedAt),
		downloadID.String(),
	)
	if err != nil {
		return fmt.Errorf("reset aggregate progress for download %s: %w", downloadID, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read progress reset result for download %s: %w", downloadID, err)
	}
	if updated != 1 {
		return fmt.Errorf("%w: %s", ErrDownloadNotFound, downloadID)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit progress reset for download %s: %w", downloadID, err)
	}
	committed = true

	return nil
}

func validateChunks(downloadID model.DownloadID, chunks []model.DownloadChunk) error {
	position := int64(0)
	for index, chunk := range chunks {
		if chunk.DownloadID != downloadID || chunk.Index != index || chunk.Start != position ||
			chunk.End < chunk.Start || chunk.DownloadedBytes < 0 || chunk.DownloadedBytes > chunk.Size() {
			return fmt.Errorf("invalid chunk %d for download %s", index, downloadID)
		}
		position = chunk.End + 1
	}

	return nil
}
