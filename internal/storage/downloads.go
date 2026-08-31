package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kristyancarvalho/argo/internal/model"
)

const downloadColumns = `id, url, destination, filename, total_size, downloaded_bytes,
    status, priority, created_at, updated_at, started_at, completed_at,
    etag, last_modified, error_message`

type scanner interface {
	Scan(destinations ...any) error
}

func (store *Store) CreateDownload(ctx context.Context, download model.Download) error {
	if err := validateDownload(download); err != nil {
		return err
	}

	_, err := store.database.ExecContext(ctx, `INSERT INTO downloads (
        id, url, destination, filename, total_size, downloaded_bytes,
        status, priority, created_at, updated_at, started_at, completed_at,
        etag, last_modified, error_message
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		download.ID.String(),
		download.URL,
		download.Destination,
		download.Filename,
		download.TotalSize,
		download.DownloadedBytes,
		string(download.Status),
		string(download.Priority),
		formatTime(download.CreatedAt),
		formatTime(download.UpdatedAt),
		nullableTime(download.StartedAt),
		nullableTime(download.CompletedAt),
		download.ETag,
		download.LastModified,
		download.Error,
	)
	if err != nil {
		return fmt.Errorf("create download %s: %w", download.ID, err)
	}

	return nil
}

func (store *Store) Download(ctx context.Context, id model.DownloadID) (model.Download, error) {
	if _, err := model.ParseDownloadID(id.String()); err != nil {
		return model.Download{}, err
	}

	row := store.database.QueryRowContext(
		ctx,
		"SELECT "+downloadColumns+" FROM downloads WHERE id = ?",
		id.String(),
	)
	download, err := scanDownload(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Download{}, fmt.Errorf("%w: %s", ErrDownloadNotFound, id)
	}
	if err != nil {
		return model.Download{}, fmt.Errorf("read download %s: %w", id, err)
	}

	return download, nil
}

func (store *Store) Downloads(ctx context.Context) ([]model.Download, error) {
	rows, err := store.database.QueryContext(
		ctx,
		"SELECT "+downloadColumns+" FROM downloads ORDER BY created_at, id",
	)
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}

	downloads := make([]model.Download, 0)
	for rows.Next() {
		download, err := scanDownload(rows)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("scan download: %w", err), rows.Close())
		}
		downloads = append(downloads, download)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Join(fmt.Errorf("iterate downloads: %w", err), rows.Close())
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close download rows: %w", err)
	}

	return downloads, nil
}

func (store *Store) UpdateDownloadProgress(
	ctx context.Context,
	id model.DownloadID,
	downloaded int64,
	updatedAt time.Time,
) error {
	if downloaded < 0 {
		return InvalidProgressError{Downloaded: downloaded, Total: -1}
	}

	result, err := store.database.ExecContext(ctx, `UPDATE downloads
        SET downloaded_bytes = ?, updated_at = ?
        WHERE id = ? AND (total_size = -1 OR ? <= total_size)`,
		downloaded,
		formatTime(updatedAt),
		id.String(),
		downloaded,
	)
	if err != nil {
		return fmt.Errorf("update progress for download %s: %w", id, err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read progress update result for download %s: %w", id, err)
	}
	if updated == 1 {
		return nil
	}

	var total int64
	if err := store.database.QueryRowContext(
		ctx,
		"SELECT total_size FROM downloads WHERE id = ?",
		id.String(),
	).Scan(&total); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrDownloadNotFound, id)
	} else if err != nil {
		return fmt.Errorf("read total size for download %s: %w", id, err)
	}

	return InvalidProgressError{Downloaded: downloaded, Total: total}
}

func (store *Store) UpdateDownloadStatus(
	ctx context.Context,
	id model.DownloadID,
	status model.Status,
	updatedAt time.Time,
	errorMessage string,
) error {
	if _, err := model.ParseStatus(string(status)); err != nil {
		return err
	}

	transaction, err := store.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin status update for download %s: %w", id, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var currentValue string
	if err := transaction.QueryRowContext(
		ctx,
		"SELECT status FROM downloads WHERE id = ?",
		id.String(),
	).Scan(&currentValue); errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: %s", ErrDownloadNotFound, id)
	} else if err != nil {
		return fmt.Errorf("read status for download %s: %w", id, err)
	}
	current, err := model.ParseStatus(currentValue)
	if err != nil {
		return fmt.Errorf("decode status for download %s: %w", id, err)
	}
	if err := model.ValidateTransition(current, status); err != nil {
		return err
	}

	formattedTime := formatTime(updatedAt)
	if _, err := transaction.ExecContext(ctx, `UPDATE downloads SET
        status = ?,
        updated_at = ?,
        error_message = ?,
        started_at = CASE WHEN ? = 'downloading' AND started_at IS NULL THEN ? ELSE started_at END,
        completed_at = CASE WHEN ? = 'completed' THEN ? ELSE completed_at END
        WHERE id = ?`,
		string(status),
		formattedTime,
		errorMessage,
		string(status),
		formattedTime,
		string(status),
		formattedTime,
		id.String(),
	); err != nil {
		return fmt.Errorf("update status for download %s: %w", id, err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit status update for download %s: %w", id, err)
	}
	committed = true

	return nil
}

func validateDownload(download model.Download) error {
	if _, err := model.ParseDownloadID(download.ID.String()); err != nil {
		return err
	}
	if _, err := model.ParseStatus(string(download.Status)); err != nil {
		return err
	}
	if _, err := model.ParsePriority(string(download.Priority)); err != nil {
		return err
	}
	if download.TotalSize < -1 || download.DownloadedBytes < 0 ||
		(download.TotalSize >= 0 && download.DownloadedBytes > download.TotalSize) {
		return InvalidProgressError{Downloaded: download.DownloadedBytes, Total: download.TotalSize}
	}

	return nil
}

func scanDownload(source scanner) (model.Download, error) {
	var download model.Download
	var identifier string
	var status string
	var priority string
	var createdAt string
	var updatedAt string
	var startedAt sql.NullString
	var completedAt sql.NullString

	if err := source.Scan(
		&identifier,
		&download.URL,
		&download.Destination,
		&download.Filename,
		&download.TotalSize,
		&download.DownloadedBytes,
		&status,
		&priority,
		&createdAt,
		&updatedAt,
		&startedAt,
		&completedAt,
		&download.ETag,
		&download.LastModified,
		&download.Error,
	); err != nil {
		return model.Download{}, err
	}

	var err error
	if download.ID, err = model.ParseDownloadID(identifier); err != nil {
		return model.Download{}, err
	}
	if download.Status, err = model.ParseStatus(status); err != nil {
		return model.Download{}, err
	}
	if download.Priority, err = model.ParsePriority(priority); err != nil {
		return model.Download{}, err
	}
	if download.CreatedAt, err = parseTime(createdAt); err != nil {
		return model.Download{}, fmt.Errorf("parse created time: %w", err)
	}
	if download.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return model.Download{}, fmt.Errorf("parse updated time: %w", err)
	}
	if startedAt.Valid {
		if download.StartedAt, err = parseTime(startedAt.String); err != nil {
			return model.Download{}, fmt.Errorf("parse started time: %w", err)
		}
	}
	if completedAt.Valid {
		if download.CompletedAt, err = parseTime(completedAt.String); err != nil {
			return model.Download{}, fmt.Errorf("parse completed time: %w", err)
		}
	}

	return download, nil
}

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}
