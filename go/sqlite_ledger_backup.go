package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

const sqliteBackupDatabase = "ledger.sqlite"
const sqliteBackupManifest = "manifest.json"

type sqliteLedgerBackup struct {
	Format        int       `json:"format"`
	ApplicationID int       `json:"application_id"`
	Schema        int       `json:"schema"`
	Generation    int64     `json:"generation"`
	Bytes         int64     `json:"bytes"`
	SHA256        string    `json:"sha256"`
	CreatedAt     time.Time `json:"created_at"`
}

// Backup takes a consistent SQLite snapshot, including committed WAL content.
// The exclusive directory and last-published manifest distinguish a complete
// backup from an interrupted one. Existing paths are never replaced. It does
// not activate the SQLite backend or include external model price files.
func (s *sqliteLedger) Backup(parent context.Context, directory string) (backup sqliteLedgerBackup, err error) {
	defer func() {
		if err != nil && parent.Err() != nil {
			err = parent.Err()
		}
	}()
	ctx, cancel := s.operationContext(parent, 0)
	defer cancel()
	if err = ctx.Err(); err != nil {
		return backup, err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return backup, err
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return backup, err
	}
	complete := false
	defer func() {
		if !complete {
			// Remove only our known files. Never recursively remove an unexpected
			// file added to the directory by another actor.
			for _, name := range []string{sqliteBackupDatabase, sqliteBackupManifest, sqliteBackupManifest + ".tmp"} {
				_ = os.Remove(filepath.Join(directory, name))
			}
			_ = os.Remove(directory)
		}
	}()
	path := filepath.Join(directory, sqliteBackupDatabase)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return backup, err
	}
	if err = file.Close(); err != nil {
		return backup, err
	}
	// VACUUM INTO uses SQLite's own snapshot machinery and writes a standalone
	// database. Copying the active main file would miss data still in the WAL.
	if _, err = s.writer.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return backup, err
	}
	backup, err = inspectSQLiteBackupDatabase(ctx, path)
	if err != nil {
		return backup, err
	}
	backup.Format = 1
	backup.CreatedAt = time.Now().UTC()
	if backup.SHA256, backup.Bytes, err = hashSQLiteBackupFile(ctx, path); err != nil {
		return backup, err
	}
	file, err = os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return backup, err
	}
	err = errors.Join(file.Sync(), file.Close())
	if err != nil {
		return backup, err
	}
	raw, err := json.Marshal(backup)
	if err != nil {
		return backup, err
	}
	temporary := filepath.Join(directory, sqliteBackupManifest+".tmp")
	file, err = os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return backup, err
	}
	_, writeErr := file.Write(raw)
	err = errors.Join(writeErr, file.Sync(), file.Close())
	if err != nil {
		return backup, err
	}
	if err = ctx.Err(); err != nil {
		return backup, err
	}
	if err = os.Rename(temporary, filepath.Join(directory, sqliteBackupManifest)); err != nil {
		return backup, err
	}
	if err = syncDir(directory); err != nil {
		return backup, err
	}
	if err = syncDir(filepath.Dir(directory)); err != nil {
		return backup, err
	}
	complete = true
	return backup, nil
}

func openSQLiteBackupFile(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("sqlite backup requires a regular, non-symlink file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, errors.New("sqlite backup file changed while opening")
	}
	return file, nil
}

func hashSQLiteBackupFile(ctx context.Context, path string) (string, int64, error) {
	file, err := openSQLiteBackupFile(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	sum, err := sqliteMigrationHash(ctx, file)
	if err != nil {
		return "", 0, err
	}
	after, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", 0, errors.New("sqlite backup changed while hashing")
	}
	return sum, after.Size(), nil
}

// Only for closed standalone backup files; immutable=1 intentionally does not
// follow WAL. Reject sidecars instead of accidentally accepting an active DB.
func inspectSQLiteBackupDatabase(ctx context.Context, path string) (backup sqliteLedgerBackup, err error) {
	for _, suffix := range []string{"-wal", "-journal"} {
		if _, statErr := os.Lstat(path + suffix); !errors.Is(statErr, os.ErrNotExist) {
			return backup, errors.New("sqlite backup has an active journal")
		}
	}
	file, err := openSQLiteBackupFile(path)
	if err != nil {
		return backup, err
	}
	if err = file.Close(); err != nil {
		return backup, err
	}
	parsed, err := url.Parse(sqliteLedgerDSN(path, true))
	if err != nil {
		return backup, err
	}
	query := parsed.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	parsed.RawQuery = query.Encode()
	db, err := sql.Open(sqliteLedgerDriverName, parsed.String())
	if err != nil {
		return backup, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&backup.ApplicationID); err != nil {
		return backup, err
	}
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&backup.Schema); err != nil {
		return backup, err
	}
	if backup.ApplicationID != sqliteLedgerApplicationID || backup.Schema != sqliteLedgerSchemaVersion {
		return backup, errors.New("unsupported sqlite backup application/schema")
	}
	if err = db.QueryRowContext(ctx, "SELECT generation FROM ledger_meta WHERE singleton=1").Scan(&backup.Generation); err != nil {
		return backup, err
	}
	var check string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&check); err != nil {
		return backup, err
	}
	if check != "ok" {
		return backup, fmt.Errorf("sqlite backup integrity: %s", check)
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return backup, err
	}
	defer rows.Close()
	if rows.Next() {
		return backup, errors.New("sqlite backup foreign key check failed")
	}
	return backup, rows.Err()
}

func verifySQLiteLedgerBackup(ctx context.Context, directory string) (backup sqliteLedgerBackup, err error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return backup, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return backup, errors.New("sqlite backup must be a non-symlink directory")
	}
	file, err := openSQLiteBackupFile(filepath.Join(directory, sqliteBackupManifest))
	if err != nil {
		return backup, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, 16385))
	err = errors.Join(readErr, file.Close())
	if err != nil {
		return backup, err
	}
	if len(raw) > 16384 {
		return backup, errors.New("sqlite backup manifest exceeds size limit")
	}
	if err = json.Unmarshal(raw, &backup); err != nil {
		return backup, err
	}
	if backup.Format != 1 || backup.Bytes <= 0 || len(backup.SHA256) != 64 || backup.CreatedAt.IsZero() {
		return backup, errors.New("invalid sqlite backup manifest")
	}
	path := filepath.Join(directory, sqliteBackupDatabase)
	hash, size, err := hashSQLiteBackupFile(ctx, path)
	if err != nil {
		return backup, err
	}
	if hash != backup.SHA256 || size != backup.Bytes {
		return backup, errors.New("sqlite backup checksum or size mismatch")
	}
	actual, err := inspectSQLiteBackupDatabase(ctx, path)
	if err != nil {
		return backup, err
	}
	if backup.Schema != actual.Schema || backup.ApplicationID != actual.ApplicationID || backup.Generation != actual.Generation {
		return backup, errors.New("sqlite backup manifest does not match database")
	}
	return backup, nil
}

// Restore into a new, inactive path only. The caller must explicitly stop the
// runtime before selecting it. A checked temporary file is linked into place
// without overwriting even a destination created concurrently.
func restoreSQLiteLedgerBackup(ctx context.Context, directory, destination string) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	backup, err := verifySQLiteLedgerBackup(ctx, directory)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, statErr := os.Lstat(destination + suffix); !errors.Is(statErr, os.ErrNotExist) {
			return errors.New("sqlite restore destination already exists or is inaccessible")
		}
	}
	source, err := openSQLiteBackupFile(filepath.Join(directory, sqliteBackupDatabase))
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.CreateTemp(filepath.Dir(destination), ".cpa-sqlite-restore-*")
	if err != nil {
		return err
	}
	temporary := target.Name()
	defer os.Remove(temporary)
	hash := sha256.New()
	written, copyErr := io.CopyBuffer(&exportContextWriter{ctx: ctx, writer: io.MultiWriter(target, hash)}, source, make([]byte, sqliteMigrationChunkBytes))
	err = errors.Join(copyErr, target.Sync(), target.Close())
	if err != nil {
		return err
	}
	if written != backup.Bytes || hex.EncodeToString(hash.Sum(nil)) != backup.SHA256 {
		return errors.New("sqlite backup changed during restore")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Link(temporary, destination); err != nil {
		return err
	}
	return syncDir(filepath.Dir(destination))
}
