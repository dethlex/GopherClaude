// Package atomicfile writes small JSON state files so that a crash never
// leaves a half-written file behind: the data goes to a temp file beside the
// target and is renamed over it.
package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const dirMode = 0o700

// WriteJSON encodes v as indented JSON and replaces path atomically, with
// the given file mode. Parent directories are created as needed.
func WriteJSON(path string, v any, mode os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirMode); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)

		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		return cleanup(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)

		return err
	}

	return os.Rename(tmpName, path)
}
