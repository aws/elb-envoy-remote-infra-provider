// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
//
// Copied from https://github.com/envoyproxy/gateway/blob/v1.10.0/internal/filewatcher/filewatcher.go
// because internal packages can't be imported from another module.
// Modified by Amazon.

package filewatcher

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// FileWatcher watches a set of files and delivers fsnotify events per path.
type FileWatcher interface {
	Add(path string) error
	Remove(path string) error
	Close() error
	Events(path string) chan fsnotify.Event
	Errors(path string) chan error
}

type fileWatcher struct {
	mu      sync.RWMutex
	workers map[string]*workerState
	funcs   *patchTable
}

type workerState struct {
	worker *worker
	count  int
}

// patchTable holds replaceable functions for testing.
type patchTable struct {
	newWatcher     func() (*fsnotify.Watcher, error)
	addWatcherPath func(*fsnotify.Watcher, string) error
}

// NewWatcher returns a FileWatcher backed by fsnotify.
func NewWatcher() FileWatcher {
	return &fileWatcher{
		workers: map[string]*workerState{},
		funcs: &patchTable{
			newWatcher: fsnotify.NewWatcher,
			addWatcherPath: func(w *fsnotify.Watcher, path string) error {
				return w.Add(path)
			},
		},
	}
}

func (fw *fileWatcher) Close() error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	for _, ws := range fw.workers {
		ws.worker.terminate()
	}
	fw.workers = nil
	return nil
}

func (fw *fileWatcher) Add(path string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	ws, cleanedPath, _, err := fw.getWorker(path)
	if err != nil {
		return err
	}
	if err = ws.worker.addPath(cleanedPath); err == nil {
		ws.count++
	}
	return err
}

func (fw *fileWatcher) Remove(path string) error {
	fw.mu.Lock()
	defer fw.mu.Unlock()

	ws, cleanedPath, parentPath, err := fw.getWorker(path)
	if err != nil {
		return err
	}
	if err = ws.worker.removePath(cleanedPath); err == nil {
		ws.count--
		if ws.count == 0 {
			ws.worker.terminate()
			delete(fw.workers, parentPath)
		}
	}
	return err
}

func (fw *fileWatcher) Events(path string) chan fsnotify.Event {
	fw.mu.RLock()
	defer fw.mu.RUnlock()

	ws, cleanedPath, err := fw.findWorker(path)
	if err != nil {
		return nil
	}
	return ws.worker.eventChannel(cleanedPath)
}

func (fw *fileWatcher) Errors(path string) chan error {
	fw.mu.RLock()
	defer fw.mu.RUnlock()

	ws, cleanedPath, err := fw.findWorker(path)
	if err != nil {
		return nil
	}
	return ws.worker.errorChannel(cleanedPath)
}

func (fw *fileWatcher) getWorker(path string) (*workerState, string, string, error) {
	if fw.workers == nil {
		return nil, "", "", errors.New("using a closed watcher")
	}
	cleanedPath, parentPath := getPath(path)
	ws, exists := fw.workers[parentPath]
	if !exists {
		wk, err := newWorker(parentPath, fw.funcs)
		if err != nil {
			return nil, "", "", err
		}
		ws = &workerState{worker: wk}
		fw.workers[parentPath] = ws
	}
	return ws, cleanedPath, parentPath, nil
}

func (fw *fileWatcher) findWorker(path string) (*workerState, string, error) {
	if fw.workers == nil {
		return nil, "", errors.New("using a closed watcher")
	}
	cleanedPath, parentPath := getPath(path)
	ws, exists := fw.workers[parentPath]
	if !exists {
		return nil, "", fmt.Errorf("no path registered for %s", path)
	}
	return ws, cleanedPath, nil
}

func getPath(path string) (string, string) {
	cleanedPath := filepath.Clean(path)
	parentPath, _ := filepath.Split(cleanedPath)
	if f, err := os.Lstat(cleanedPath); err == nil && f.IsDir() {
		parentPath = cleanedPath
	}
	return cleanedPath, parentPath
}
