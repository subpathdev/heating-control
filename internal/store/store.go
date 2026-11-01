// Package store saves the controller's state to a JSON file.
package store

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/subpathdev/heating-control/internal/controller"
)

// File implements controller.Store.
type File struct {
	path string
}

// New returns a File that saves to path.
func New(path string) *File {
	return &File{path: path}
}

// Load returns the saved records. A file that doesn't exist yet means none.
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
		return nil, err
	}
	return records, nil
}

// Save replaces the file in one step, so a crash leaves the old or the new one.
func (f *File) Save(records map[string]controller.RoomRecord) error {
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
