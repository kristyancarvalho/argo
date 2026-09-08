package downloader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kristyancarvalho/argo/internal/model"
	"github.com/kristyancarvalho/argo/internal/xdg"
	"golang.org/x/sys/unix"
)

func DefaultPartsDirectory() (string, error) {
	state, configured, err := xdg.EnvironmentDirectory("XDG_STATE_HOME")
	if err != nil {
		return "", err
	}
	if configured {
		return filepath.Join(state, "argo", "parts"), nil
	}
	home, err := xdg.HomeDirectory()
	if err != nil {
		return "", fmt.Errorf("determine home directory for partial storage: %w", err)
	}

	return filepath.Join(home, ".local", "state", "argo", "parts"), nil
}

func (engine *Engine) partialPath(identifier model.DownloadID) string {
	return filepath.Join(engine.parts, identifier.String()+".part")
}

func partialName(identifier model.DownloadID) string {
	return identifier.String() + ".part"
}

type finalizationRecord struct {
	Candidate string `json:"candidate"`
	Staging   string `json:"staging"`
	Size      int64  `json:"size"`
	Ready     bool   `json:"ready"`
}

type finalizationInterruptedError struct {
	err error
}

func (err finalizationInterruptedError) Error() string {
	return err.err.Error()
}

func (err finalizationInterruptedError) Unwrap() error {
	return err.err
}

func finalizationName(identifier model.DownloadID) string {
	return identifier.String() + ".finalize"
}

func (engine *Engine) RemovePartial(identifier model.DownloadID) error {
	if _, err := model.ParseDownloadID(identifier.String()); err != nil {
		return err
	}
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.Close()
	}()
	if err := unix.Unlinkat(int(directory.Fd()), partialName(identifier), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove partial file for download %s: %w", identifier, err)
	}

	return nil
}

func (engine *Engine) preparePartial(download model.Download) error {
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return err
	}
	defer func() {
		_ = directory.Close()
	}()
	var current unix.Stat_t
	err = unix.Fstatat(int(directory.Fd()), partialName(download.ID), &current, unix.AT_SYMLINK_NOFOLLOW)
	if err == nil {
		if err := validatePartialStat(&current); err != nil {
			return fmt.Errorf("inspect partial file: %w", err)
		}
		return nil
	} else if !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("inspect partial file: %w", err)
	}
	if !filepath.IsAbs(download.Destination) {
		return nil
	}
	legacy := filepath.Join(download.Destination, ".argo-"+download.ID.String()+".part")
	info, err := os.Lstat(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect legacy partial file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	legacyFile, err := os.OpenFile(legacy, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("open legacy partial file: %w", err)
	}
	defer func() {
		_ = legacyFile.Close()
	}()
	if err := validatePartialFile(legacyFile); err != nil {
		return fmt.Errorf("validate legacy partial file: %w", err)
	}
	openedInfo, err := legacyFile.Stat()
	if err != nil {
		return fmt.Errorf("inspect legacy partial descriptor: %w", err)
	}
	if !os.SameFile(info, openedInfo) {
		return fmt.Errorf("legacy partial path changed during migration")
	}
	if err := publishPartial(directory, partialName(download.ID), legacyFile); err != nil {
		return fmt.Errorf("migrate legacy partial file: %w", err)
	}
	if err := removeIfSame(legacy, legacyFile); err != nil {
		return fmt.Errorf("remove migrated legacy partial file: %w", err)
	}

	return nil
}

func (engine *Engine) finalize(download model.Download, initial string, partial *os.File) (string, error) {
	directory, err := engine.validatePartialBinding(download.ID, partial)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = directory.Close()
	}()
	info, err := partial.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect finalized partial file: %w", err)
	}
	record, err := engine.newFinalizationRecord(download, initial, info.Size())
	if err != nil {
		return "", err
	}
	if err := engine.writeFinalizationRecord(directory, download.ID, record); err != nil {
		return "", err
	}
	return engine.stageAndPublish(download, directory, partial, record)
}

func collisionPath(path string, index int) string {
	if index == 0 {
		return path
	}
	extension := filepath.Ext(path)
	base := path[:len(path)-len(extension)]

	return fmt.Sprintf("%s (%d)%s", base, index, extension)
}

func (engine *Engine) copyNoReplace(input *os.File, destination string) (result error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind partial file: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	complete := false
	if err := engine.finalizationCheckpoint("created"); err != nil {
		_ = output.Close()
		complete = true
		return err
	}
	defer func() {
		if !complete {
			result = errors.Join(result, output.Close())
			if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}()
	buffer := make([]byte, copyBufferSize)
	checkpointed := false
	for {
		read, readErr := input.Read(buffer)
		if read > 0 {
			written, writeErr := output.Write(buffer[:read])
			if writeErr != nil {
				return writeErr
			}
			if written != read {
				return io.ErrShortWrite
			}
			if !checkpointed {
				checkpointed = true
				if err := engine.finalizationCheckpoint("copying"); err != nil {
					_ = output.Close()
					complete = true
					return err
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := engine.finalizationCheckpoint("synced"); err != nil {
		_ = output.Close()
		complete = true
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	complete = true

	return nil
}

func (engine *Engine) newFinalizationRecord(download model.Download, initial string, size int64) (finalizationRecord, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return finalizationRecord{}, fmt.Errorf("generate finalization staging name: %w", err)
	}
	staging := ".argo-" + download.ID.String() + "-" + hex.EncodeToString(nonce) + ".finalizing"
	for index := 0; ; index++ {
		candidate := collisionPath(initial, index)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return finalizationRecord{Candidate: filepath.Base(candidate), Staging: staging, Size: size}, nil
		}
		if err != nil {
			return finalizationRecord{}, fmt.Errorf("inspect final destination: %w", err)
		}
	}
}

func (engine *Engine) stageAndPublish(download model.Download, stateDirectory *os.File, partial *os.File, record finalizationRecord) (string, error) {
	staging := filepath.Join(download.Destination, record.Staging)
	candidate := filepath.Join(download.Destination, record.Candidate)
	if err := engine.linkOrCopyStaging(partial, staging); err != nil {
		var interrupted finalizationInterruptedError
		if !errors.As(err, &interrupted) {
			_ = engine.removeFinalizationArtifacts(download, stateDirectory, record, false)
		}
		return "", fmt.Errorf("stage finalized download: %w", err)
	}
	record.Ready = true
	if err := engine.writeFinalizationRecord(stateDirectory, download.ID, record); err != nil {
		return "", err
	}
	for {
		err := os.Link(staging, candidate)
		if errors.Is(err, fs.ErrExist) {
			next, nextErr := nextCollisionCandidate(download.Destination, download.Filename)
			if nextErr != nil {
				return "", nextErr
			}
			record.Candidate = filepath.Base(next)
			candidate = next
			if err := engine.writeFinalizationRecord(stateDirectory, download.ID, record); err != nil {
				return "", err
			}
			continue
		}
		if err != nil {
			return "", fmt.Errorf("publish finalized download: %w", err)
		}
		break
	}
	if err := syncDirectory(download.Destination); err != nil {
		return "", fmt.Errorf("sync final destination directory: %w", err)
	}
	if err := engine.finalizationCheckpoint("published"); err != nil {
		return "", err
	}

	return candidate, nil
}

func (engine *Engine) linkOrCopyStaging(source *os.File, staging string) error {
	err := unix.Linkat(int(source.Fd()), "", unix.AT_FDCWD, staging, unix.AT_EMPTY_PATH)
	if err == nil {
		return engine.finalizationCheckpoint("created")
	}
	if errors.Is(err, unix.EXDEV) {
		return engine.copyNoReplace(source, staging)
	}

	return err
}

func (engine *Engine) finalizationCheckpoint(name string) error {
	if engine.checkpoint == nil {
		return nil
	}
	if err := engine.checkpoint(name); err != nil {
		return finalizationInterruptedError{err: err}
	}

	return nil
}

func nextCollisionCandidate(destination string, filename string) (string, error) {
	initial := filepath.Join(destination, filename)
	for index := 0; ; index++ {
		candidate := collisionPath(initial, index)
		_, err := os.Lstat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("inspect final destination: %w", err)
		}
	}
}

func (engine *Engine) writeFinalizationRecord(directory *os.File, identifier model.DownloadID, record finalizationRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode finalization record: %w", err)
	}
	temporary := finalizationName(identifier) + ".tmp"
	_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
	fd, err := unix.Openat(int(directory.Fd()), temporary, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create finalization record: %w", err)
	}
	file := os.NewFile(uintptr(fd), temporary)
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		return fmt.Errorf("write finalization record: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		return fmt.Errorf("sync finalization record: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		return fmt.Errorf("close finalization record: %w", err)
	}
	if err := unix.Renameat(int(directory.Fd()), temporary, int(directory.Fd()), finalizationName(identifier)); err != nil {
		_ = unix.Unlinkat(int(directory.Fd()), temporary, 0)
		return fmt.Errorf("publish finalization record: %w", err)
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync partial directory: %w", err)
	}

	return nil
}

func (engine *Engine) RecoverFinalizations(ctx context.Context, downloads []model.Download) error {
	for _, download := range downloads {
		record, exists, err := engine.readFinalizationRecord(download.ID)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}
		if err := validateFinalizationRecord(download, record); err != nil {
			return err
		}
		if download.Status == model.StatusCompleted {
			if err := engine.cleanupFinalization(download, record); err != nil {
				return err
			}
			continue
		}
		if download.Status != model.StatusDownloading {
			continue
		}
		finalPath, err := engine.recoverFinalization(download, record)
		if err != nil {
			return err
		}
		current, exists, err := engine.readFinalizationRecord(download.ID)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("recovered finalization record for download %s is missing", download.ID)
		}
		if err := validateFinalizationRecord(download, current); err != nil {
			return err
		}
		if err := engine.completeFinalization(ctx, download, finalPath, current); err != nil {
			return err
		}
	}

	return nil
}

func (engine *Engine) readFinalizationRecord(identifier model.DownloadID) (finalizationRecord, bool, error) {
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return finalizationRecord{}, false, err
	}
	defer func() { _ = directory.Close() }()
	fd, err := unix.Openat(int(directory.Fd()), finalizationName(identifier), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return finalizationRecord{}, false, nil
	}
	if err != nil {
		return finalizationRecord{}, false, fmt.Errorf("open finalization record: %w", err)
	}
	file := os.NewFile(uintptr(fd), finalizationName(identifier))
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return finalizationRecord{}, false, fmt.Errorf("inspect finalization record: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 4096 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return finalizationRecord{}, false, fmt.Errorf("unsafe finalization record for download %s", identifier)
	}
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil {
		return finalizationRecord{}, false, fmt.Errorf("read finalization record: %w", err)
	}
	var record finalizationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return finalizationRecord{}, false, fmt.Errorf("decode finalization record: %w", err)
	}

	return record, true, nil
}

func validateFinalizationRecord(download model.Download, record finalizationRecord) error {
	prefix := ".argo-" + download.ID.String() + "-"
	if record.Candidate == "" || filepath.Base(record.Candidate) != record.Candidate {
		return fmt.Errorf("invalid finalization destination for download %s", download.ID)
	}
	if filepath.Base(record.Staging) != record.Staging || !strings.HasPrefix(record.Staging, prefix) || !strings.HasSuffix(record.Staging, ".finalizing") {
		return fmt.Errorf("invalid finalization staging name for download %s", download.ID)
	}
	if record.Size < 0 {
		return fmt.Errorf("invalid finalization size for download %s", download.ID)
	}

	return nil
}

func (engine *Engine) recoverFinalization(download model.Download, record finalizationRecord) (string, error) {
	staging := filepath.Join(download.Destination, record.Staging)
	candidate := filepath.Join(download.Destination, record.Candidate)
	stagingInfo, err := os.Lstat(staging)
	if errors.Is(err, os.ErrNotExist) || !record.Ready {
		if err == nil {
			if removeErr := removeOwnedStaging(staging, stagingInfo); removeErr != nil {
				return "", removeErr
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect finalization staging file: %w", err)
		}
		return engine.restartFinalization(download, record)
	}
	if err != nil {
		return "", fmt.Errorf("inspect finalization staging file: %w", err)
	}
	if err := validateStagingInfo(stagingInfo, record.Size); err != nil {
		return "", err
	}
	candidateInfo, candidateErr := os.Lstat(candidate)
	if candidateErr == nil {
		if os.SameFile(stagingInfo, candidateInfo) {
			return candidate, nil
		}
		next, err := nextCollisionCandidate(download.Destination, download.Filename)
		if err != nil {
			return "", err
		}
		record.Candidate = filepath.Base(next)
		candidate = next
		stateDirectory, err := secureDirectory(engine.parts)
		if err != nil {
			return "", err
		}
		defer func() { _ = stateDirectory.Close() }()
		if err := engine.writeFinalizationRecord(stateDirectory, download.ID, record); err != nil {
			return "", err
		}
	} else if !errors.Is(candidateErr, os.ErrNotExist) {
		return "", fmt.Errorf("inspect finalization destination: %w", candidateErr)
	}
	if err := os.Link(staging, candidate); err != nil {
		return "", fmt.Errorf("publish recovered download: %w", err)
	}
	if err := syncDirectory(download.Destination); err != nil {
		return "", fmt.Errorf("sync recovered destination directory: %w", err)
	}

	return candidate, nil
}

func (engine *Engine) restartFinalization(download model.Download, record finalizationRecord) (string, error) {
	stateDirectory, err := secureDirectory(engine.parts)
	if err != nil {
		return "", err
	}
	if err := unix.Unlinkat(int(stateDirectory.Fd()), finalizationName(download.ID), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		_ = stateDirectory.Close()
		return "", fmt.Errorf("remove interrupted finalization record: %w", err)
	}
	if err := stateDirectory.Sync(); err != nil {
		_ = stateDirectory.Close()
		return "", fmt.Errorf("sync partial directory: %w", err)
	}
	_ = stateDirectory.Close()
	partial, err := engine.openPartialFile(download.ID, unix.O_RDWR)
	if err != nil {
		return "", fmt.Errorf("open partial file for finalization recovery: %w", err)
	}
	defer func() { _ = partial.Close() }()
	info, err := partial.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect partial file for finalization recovery: %w", err)
	}
	if info.Size() != record.Size {
		return "", fmt.Errorf("partial file for download %s has size %d, expected %d", download.ID, info.Size(), record.Size)
	}

	return engine.finalize(download, filepath.Join(download.Destination, download.Filename), partial)
}

func validateStagingInfo(info os.FileInfo, size int64) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink < 1 || stat.Nlink > 3 {
		return fmt.Errorf("unsafe finalization staging file")
	}
	if info.Size() != size {
		return fmt.Errorf("finalization staging file has size %d, expected %d", info.Size(), size)
	}

	return nil
}

func removeOwnedStaging(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink < 1 || stat.Nlink > 3 {
		return fmt.Errorf("refuse to remove unsafe finalization staging file")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove finalization staging file: %w", err)
	}

	return nil
}

func (engine *Engine) completeFinalization(ctx context.Context, download model.Download, finalPath string, record finalizationRecord) error {
	filename := filepath.Base(finalPath)
	if filename != download.Filename {
		if err := engine.store.UpdateDownloadFilename(ctx, download.ID, filename, engine.now()); err != nil {
			return err
		}
	}
	if err := engine.store.UpdateDownloadStatus(ctx, download.ID, model.StatusCompleted, engine.now(), ""); err != nil {
		return err
	}

	return engine.cleanupFinalization(download, record)
}

func (engine *Engine) completeCurrentFinalization(ctx context.Context, download model.Download, finalPath string) error {
	record, exists, err := engine.readFinalizationRecord(download.ID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("finalization record for download %s is missing", download.ID)
	}
	if err := validateFinalizationRecord(download, record); err != nil {
		return err
	}

	return engine.completeFinalization(ctx, download, finalPath, record)
}

func (engine *Engine) cleanupFinalization(download model.Download, record finalizationRecord) error {
	stateDirectory, err := secureDirectory(engine.parts)
	if err != nil {
		return err
	}
	defer func() { _ = stateDirectory.Close() }()
	if err := engine.removeFinalizationArtifacts(download, stateDirectory, record, true); err != nil {
		return err
	}
	if err := stateDirectory.Sync(); err != nil {
		return fmt.Errorf("sync partial directory after finalization: %w", err)
	}
	if err := syncDirectory(download.Destination); err != nil {
		return fmt.Errorf("sync destination after finalization cleanup: %w", err)
	}

	return nil
}

func (engine *Engine) removeFinalizationArtifacts(download model.Download, stateDirectory *os.File, record finalizationRecord, removePartial bool) error {
	staging := filepath.Join(download.Destination, record.Staging)
	if info, err := os.Lstat(staging); err == nil {
		if err := removeOwnedStaging(staging, info); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect finalization staging file: %w", err)
	}
	if removePartial {
		if err := unix.Unlinkat(int(stateDirectory.Fd()), partialName(download.ID), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("remove finalized partial file: %w", err)
		}
	}
	if err := unix.Unlinkat(int(stateDirectory.Fd()), finalizationName(download.ID), 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("remove finalization record: %w", err)
	}

	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()

	return directory.Sync()
}

func secureDirectory(path string) (*os.File, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, fmt.Errorf("partial directory must be absolute")
	}
	current, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open filesystem root: %w", err)
	}
	components := strings.Split(strings.TrimPrefix(clean, string(filepath.Separator)), string(filepath.Separator))
	for index, component := range components {
		if component == "" {
			continue
		}
		next, openError := unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(openError, unix.ENOENT) {
			if mkdirError := unix.Mkdirat(current, component, 0o700); mkdirError != nil && !errors.Is(mkdirError, unix.EEXIST) {
				_ = unix.Close(current)
				return nil, fmt.Errorf("create partial directory component %s: %w", component, mkdirError)
			}
			next, openError = unix.Openat(current, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openError != nil {
			_ = unix.Close(current)
			return nil, fmt.Errorf("open partial directory component %s without symlinks: %w", component, openError)
		}
		var stat unix.Stat_t
		if err := unix.Fstat(next, &stat); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return nil, fmt.Errorf("inspect partial directory component %s: %w", component, err)
		}
		final := index == len(components)-1
		if err := validateDirectoryStat(&stat, final); err != nil {
			_ = unix.Close(next)
			_ = unix.Close(current)
			return nil, fmt.Errorf("unsafe partial directory component %s: %w", component, err)
		}
		_ = unix.Close(current)
		current = next
	}

	return os.NewFile(uintptr(current), clean), nil
}

func validateDirectoryStat(stat *unix.Stat_t, final bool) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("not a directory")
	}
	uid := uint32(os.Geteuid())
	if stat.Uid != 0 && stat.Uid != uid {
		return fmt.Errorf("owned by user ID %d", stat.Uid)
	}
	permissions := stat.Mode & 0o7777
	if final {
		if stat.Uid != uid {
			return fmt.Errorf("final directory is not owned by the current user")
		}
		if permissions&0o022 != 0 {
			return fmt.Errorf("final directory permissions %04o allow group or other writes", permissions)
		}
		return nil
	}
	if permissions&0o022 != 0 && (stat.Uid != 0 || permissions&unix.S_ISVTX == 0) {
		return fmt.Errorf("permissions %04o allow unsafe replacement", permissions)
	}

	return nil
}

func (engine *Engine) openPartialFile(identifier model.DownloadID, flags int) (*os.File, error) {
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = directory.Close()
	}()
	fd, err := unix.Openat(int(directory.Fd()), partialName(identifier), flags|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open partial file: %w", err)
	}
	file := os.NewFile(uintptr(fd), engine.partialPath(identifier))
	if err := validatePartialFile(file); err != nil {
		_ = file.Close()
		return nil, err
	}

	return file, nil
}

func validatePartialFile(file *os.File) error {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return fmt.Errorf("inspect partial descriptor: %w", err)
	}

	return validatePartialStat(&stat)
}

func validatePartialStat(stat *unix.Stat_t) error {
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("partial path is not a regular file")
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("partial file is owned by user ID %d", stat.Uid)
	}
	if stat.Mode&0o077 != 0 {
		return fmt.Errorf("partial file permissions allow group or other access")
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("partial file has %d filesystem links", stat.Nlink)
	}

	return nil
}

func (engine *Engine) validatePartialBinding(identifier model.DownloadID, file *os.File) (*os.File, error) {
	if err := validatePartialFile(file); err != nil {
		return nil, err
	}
	directory, err := secureDirectory(engine.parts)
	if err != nil {
		return nil, err
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), partialName(identifier), &pathStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect partial path before finalization: %w", err)
	}
	if err := validatePartialStat(&pathStat); err != nil {
		_ = directory.Close()
		return nil, err
	}
	var fileStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect partial descriptor before finalization: %w", err)
	}
	if pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino {
		_ = directory.Close()
		return nil, fmt.Errorf("partial path changed during transfer")
	}

	return directory, nil
}

func publishPartial(directory *os.File, name string, source *os.File) error {
	err := unix.Linkat(int(source.Fd()), "", int(directory.Fd()), name, unix.AT_EMPTY_PATH)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.EXDEV) {
		return err
	}
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return err
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	destination := os.NewFile(uintptr(fd), name)
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return err
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = unix.Unlinkat(int(directory.Fd()), name, 0)
		return err
	}

	return destination.Close()
}

func removeIfSame(path string, file *os.File) error {
	var pathStat unix.Stat_t
	if err := unix.Lstat(path, &pathStat); err != nil {
		return err
	}
	var fileStat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &fileStat); err != nil {
		return err
	}
	if pathStat.Dev != fileStat.Dev || pathStat.Ino != fileStat.Ino {
		return fmt.Errorf("path changed during operation")
	}

	return os.Remove(path)
}
