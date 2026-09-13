package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var errBackupProvenance = errors.New("daily backup provenance unavailable or mismatched; preserve artifacts for inspection")

// This is a prepared-capture receipt, NOT a durable completion flag. A matching
// pair must be fsynced again before any retry may report completion. The protocol
// is local-filesystem bookkeeping, not authentication against a privileged owner.
// Restore/export remain standalone SQLite and never require this receipt.
type dailyBackupProof struct {
	Protocol string `json:"protocol"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
}

func dailyBackupProofPath(path string) string { return path + ".provenance.json" }

// Publication order, under the existing directory flock:
// validated/synced snapshot -> hash the opened inode -> sync receipt -> link
// receipt no-replace -> sync directory -> link DB no-replace -> sync directory.
// A proof alone blocks the name. A complete matching pair can be reconfirmed.
// Only artifacts created by this invocation may be rolled back.
func publishDailyBackup(ctx context.Context, sourcePath, destPath string, ops *backupPublicationOps) (published bool, retErr error) {
	retErr = withBackupPublicationLock(ctx, filepath.Dir(destPath), func() (publicationErr error) {
		if _, ok := parseDailyAutomaticBackupName(filepath.Base(destPath)); !ok {
			return errBackupProvenance
		}
		for _, path := range []string{destPath, dailyBackupProofPath(destPath)} {
			if _, err := os.Lstat(path); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		f, info, err := openBackupRegular(sourcePath)
		if err != nil {
			return err
		}
		defer func() { publicationErr = errors.Join(publicationErr, f.Close()) }()
		digest, err := hashBackupFile(ctx, f)
		if err != nil {
			return err
		}
		if err := unchangedBackupFile(f, sourcePath, info); err != nil {
			return err
		}
		// The snapshot was validated by CreateSQLiteBackupFileContext. Hashing
		// and syncing this same descriptor binds the receipt to those exact bytes.
		if err := ops.fileSync(f); err != nil {
			return err
		}
		stat := info.Sys().(*syscall.Stat_t)
		proof := dailyBackupProof{"contabase-daily-v1", filepath.Base(destPath), info.Size(), digest, uint64(stat.Dev), uint64(stat.Ino)}
		data, err := json.Marshal(proof)
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(sourcePath), ".provenance-*")
		if err != nil {
			return err
		}
		defer func() { publicationErr = errors.Join(publicationErr, tmp.Close()) }()
		defer func() {
			if err := os.Remove(tmp.Name()); err != nil && !os.IsNotExist(err) {
				publicationErr = errors.Join(publicationErr, err)
			}
		}()
		if _, err := tmp.Write(data); err != nil {
			return err
		}
		if err := tmp.Chmod(0o400); err != nil {
			return err
		}
		if err := ops.fileSync(tmp); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		proofPath := dailyBackupProofPath(destPath)
		if err := os.Link(tmp.Name(), proofPath); err != nil {
			return err
		}
		if err := os.Remove(tmp.Name()); err != nil {
			return err
		}
		// On failure, remove our receipt only if no DB remains and its identity
		// is still ours. A surviving DB keeps its receipt for F05-R01 recovery.
		defer func() {
			if _, err := os.Lstat(destPath); os.IsNotExist(err) {
				if err := sameBackupFile(tmp, proofPath); err == nil {
					// Cleanup failure is never converted to success by the caller.
					if err := os.Remove(proofPath); err != nil {
						publicationErr = errors.Join(publicationErr, err)
					} else {
						publicationErr = errors.Join(publicationErr, ops.directorySync(filepath.Dir(destPath)))
					}
				}
			}
		}()
		if err := ops.directorySync(filepath.Dir(destPath)); err != nil {
			return err
		}
		if err := unchangedBackupFile(f, sourcePath, info); err != nil {
			return err
		}
		published, err = publishBackupFileLocked(ctx, sourcePath, destPath, ops)
		if err != nil {
			return err
		}
		if !published {
			return fmt.Errorf("daily backup destination appeared outside publication lock: %w", errBackupProvenance)
		}
		if err := unchangedBackupFile(f, destPath, info); err != nil {
			return err
		}
		return sameBackupFile(tmp, proofPath)
	})
	return published, retErr
}

type dailyBackupPair struct {
	file, proof         *os.File
	fileInfo, proofInfo os.FileInfo
}

func (p *dailyBackupPair) close() error { return errors.Join(p.file.Close(), p.proof.Close()) }
func (p *dailyBackupPair) checkIdentity(path string) error {
	return errors.Join(unchangedBackupFile(p.file, path, p.fileInfo), unchangedBackupFile(p.proof, dailyBackupProofPath(path), p.proofInfo))
}

// Caller holds the publication lock. No chmod/removal is performed until the
// receipt, identity and full content hash have been verified on open handles.
func openDailyBackupPair(ctx context.Context, path string) (_ *dailyBackupPair, retErr error) {
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if _, proofErr := os.Lstat(dailyBackupProofPath(path)); os.IsNotExist(proofErr) {
			return nil, nil
		}
		return nil, fmt.Errorf("orphan receipt: %w", errBackupProvenance)
	}
	f, info, err := openBackupRegular(path)
	if err != nil {
		return nil, errors.Join(errBackupProvenance, err)
	}
	defer func() {
		if retErr != nil {
			f.Close()
		}
	}()
	pf, pi, err := openBackupRegular(dailyBackupProofPath(path))
	if err != nil {
		return nil, errors.Join(errBackupProvenance, err)
	}
	defer func() {
		if retErr != nil {
			pf.Close()
		}
	}()
	if pi.Size() > 2048 {
		return nil, errBackupProvenance
	}
	data, err := io.ReadAll(io.LimitReader(pf, 2049))
	if err != nil {
		return nil, err
	}
	var proof dailyBackupProof
	if err := json.Unmarshal(data, &proof); err != nil {
		return nil, errors.Join(errBackupProvenance, err)
	}
	canonical, err := json.Marshal(proof)
	if err != nil || !bytes.Equal(data, canonical) {
		return nil, errBackupProvenance
	}
	stat := info.Sys().(*syscall.Stat_t)
	if proof.Protocol != "contabase-daily-v1" || proof.Name != filepath.Base(path) || proof.Size != info.Size() || proof.Device != uint64(stat.Dev) || proof.Inode != uint64(stat.Ino) {
		return nil, errBackupProvenance
	}
	digest, err := hashBackupFile(ctx, f)
	if err != nil {
		return nil, err
	}
	if digest != proof.SHA256 {
		return nil, errBackupProvenance
	}
	pair := &dailyBackupPair{f, pf, info, pi}
	if err := pair.checkIdentity(path); err != nil {
		return nil, err
	}
	return pair, nil
}

func confirmExistingBackup(ctx context.Context, path string, ops *backupPublicationOps) (exists bool, err error) {
	err = withBackupPublicationLock(ctx, filepath.Dir(path), func() (confirmErr error) {
		pair, err := openDailyBackupPair(ctx, path)
		if err != nil || pair == nil {
			return err
		}
		defer func() { confirmErr = errors.Join(confirmErr, pair.close()) }()
		if err := ops.fileSync(pair.file); err != nil {
			return err
		}
		if err := ops.fileSync(pair.proof); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := pair.checkIdentity(path); err != nil {
			return err
		}
		if err := ops.directorySync(filepath.Dir(path)); err != nil {
			return err
		}
		if err := pair.checkIdentity(path); err != nil {
			return err
		}
		exists = true
		return nil
	})
	return exists, err
}

func openBackupRegular(path string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Size() == 0 || !ok || stat.Nlink != 1 {
		return nil, nil, fmt.Errorf("backup candidate is not regular/nonempty or has unknown hard links: %w", errBackupProvenance)
	}
	// O_NOFOLLOW/O_NONBLOCK also reject symlink/FIFO substitution after Lstat.
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if err := unchangedBackupFile(f, path, info); err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, info, nil
}

func sameBackupFile(f *os.File, path string) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !current.Mode().IsRegular() || !os.SameFile(info, current) {
		return errBackupProvenance
	}
	return nil
}

func unchangedBackupFile(f *os.File, path string, original os.FileInfo) error {
	if err := sameBackupFile(f, path); err != nil {
		return err
	}
	current, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(original, current) || original.Size() != current.Size() || !original.ModTime().Equal(current.ModTime()) {
		return errBackupProvenance
	}
	return nil
}

func hashBackupFile(ctx context.Context, f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	buf := make([]byte, 128*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = hash.Write(buf[:n])
		}
		if err == io.EOF {
			return hex.EncodeToString(hash.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}
