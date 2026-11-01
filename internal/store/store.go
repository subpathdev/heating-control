// Package store saves the controller's state to a JSON file.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/subpathdev/heating-control/internal/controller"
)

// File implements controller.Store and health.Checker.
type File struct {
	path string

	mu      sync.Mutex
	corrupt bool
	saveErr error
}

// New returns a File that saves to path.
func New(path string) *File {
	return &File{path: path}
}

// Name implements health.Checker.
func (f *File) Name() string { return "state" }

// Check implements health.Checker. A corrupt file stays reported until a restart,
// because a room it had on record may still be off.
func (f *File) Check(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.corrupt {
		return fmt.Errorf("%s was corrupt and moved aside", f.path)
	}
	return f.saveErr
}

// Load returns the saved records. A file that doesn't exist yet means none. A
// corrupt file is moved aside, because the next save would overwrite it.
func (f *File) Load() (map[string]controller.RoomRecord, error) {
	data, err := os.ReadFile(f.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records map[string]controller.RoomRecord
	if err := json.Unmarshal(data, &records); err != nil {
		f.mu.Lock()
		f.corrupt = true
		f.mu.Unlock()
		if renameErr := os.Rename(f.path, f.path+".corrupt"); renameErr != nil {
			return nil, errors.Join(err, renameErr)
		}
		return nil, fmt.Errorf("moved to %s.corrupt: %w", f.path, err)
	}
	return records, nil
}

// Save replaces the file in one step, so a crash leaves the old or the new one.
func (f *File) Save(records map[string]controller.RoomRecord) error {
	err := f.write(records)
	f.mu.Lock()
	f.saveErr = err
	f.mu.Unlock()
	return err
}

func (f *File) write(records map[string]controller.RoomRecord) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), filepath.Base(f.path)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), f.path)
}
