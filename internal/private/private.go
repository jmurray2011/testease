// Package private makes directories only the account running testease can
// use: secret workspaces, carried bytes, staged inputs.
//
// On unix that is mode 0700, and the files written inside carry their own
// 0600 or 0400. Windows ignores modes: there a directory is given a protected
// access list naming its owner alone, and the files written inside inherit it.
package private

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// Mkdir makes one private directory. It fails with fs.ErrExist when something
// is already there: a private directory that already existed was made by
// someone else, under rules nobody checked.
func Mkdir(path string) error {
	return mkdir(path)
}

// MkdirAll makes path and any missing parents. The leaf is private when this
// makes it; one that already exists is left as it is, as os.MkdirAll does.
func MkdirAll(path string) error {
	if info, err := os.Stat(path); err == nil {
		if !info.IsDir() {
			return &fs.PathError{Op: "mkdir", Path: path, Err: errors.New("not a directory")}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := mkdir(path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

var tempSeq atomic.Uint64

// MkdirTemp makes a new private directory in dir whose name begins with
// prefix, and returns its path.
func MkdirTemp(dir, prefix string) (string, error) {
	for range 10000 {
		n := tempSeq.Add(1) ^ uint64(time.Now().UnixNano())
		path := filepath.Join(dir, prefix+strconv.FormatUint(n%1e10, 10))
		err := mkdir(path)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return "", &fs.PathError{Op: "mkdirtemp", Path: filepath.Join(dir, prefix+"*"), Err: fs.ErrExist}
}

// Verify says whether path is private: on unix, that its mode is exactly
// perm; on Windows, that only its owner is granted any access to it.
func Verify(path string, perm fs.FileMode) error {
	return verify(path, perm)
}
