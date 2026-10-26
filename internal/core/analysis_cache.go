package core

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"singularity-vpn-app/internal/ipc"
)

const analysis_cache_limit = 1024 * 1024

// restore_analysis_locked loads historical evidence, never a statement about
// the current network. A fresh measurement clears the stale flag.
func (a *Application) restore_analysis_locked() {
	c := &a.analysis
	if c.loaded {
		return
	}
	c.loaded = true
	if a.analysis_cache_path == "" || c.has {
		return
	}
	file, err := os.Open(a.analysis_cache_path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		a.logger.Warn("analysis cache unavailable")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, analysis_cache_limit+1))
	var result ipc.Analysis_result
	if err != nil || len(data) > analysis_cache_limit || json.Unmarshal(data, &result) != nil || result.Measured_at.IsZero() || result.Measured_at.After(a.now()) || len(result.Checks) == 0 {
		a.logger.Warn("analysis cache invalid")
		return
	}
	c.last, c.has, c.stale = result, true, true
}

// save_analysis_locked writes only completed measurements, atomically, under
// the service data directory's ACL. Raw responses and credentials are absent.
func (a *Application) save_analysis_locked(result ipc.Analysis_result) error {
	if a.analysis_cache_path == "" {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(a.analysis_cache_path), ".analysis-*.tmp")
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
	return os.Rename(name, a.analysis_cache_path)
}
