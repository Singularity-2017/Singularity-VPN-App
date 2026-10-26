package core

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

// err_file_too_large says a small state file is bigger than anything this
// service writes, so it is not one of ours.
var err_file_too_large = errors.New("core: the file is larger than the service ever writes")

// read_small_file reads a file the service itself keeps in its data directory,
// refusing one over limit bytes. A missing file is an os.IsNotExist error.
func read_small_file(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, err_file_too_large
	}
	return data, nil
}

// write_small_file replaces path with data atomically -- a temporary file in
// the same directory, synced, then renamed over it -- the way
// save_analysis_locked does, so a crash leaves the old file or the new one and
// never half of either. The directory's ACL is the service data directory's.
func write_small_file(path, temporary_pattern string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), temporary_pattern)
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	close_err := file.Close()
	if err != nil {
		return err
	}
	if close_err != nil {
		return close_err
	}
	return os.Rename(name, path)
}
