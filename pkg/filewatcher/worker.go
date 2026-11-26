// Copyright Envoy Gateway Authors
// SPDX-License-Identifier: Apache-2.0
// The full text of the Apache license is available in the LICENSE file at
// the root of the repo.
//
// Copied from https://github.com/envoyproxy/gateway/blob/v1.10.0/internal/filewatcher/worker.go
// because internal packages can't be imported from another module.
// Modified by Amazon.

package filewatcher

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type worker struct {
	mu sync.RWMutex

	// dirWatcher watches the parent directory of all tracked files.
	dirWatcher *fsnotify.Watcher

	// watchedFiles maps cleaned file path → fileTracker.
	watchedFiles map[string]*fileTracker

	retireTrackerCh chan *fileTracker
	terminateCh     chan bool
}

type fileTracker struct {
	events chan fsnotify.Event
	errors chan error
	hash   []byte
}

func newWorker(path string, funcs *patchTable) (*worker, error) {
	dirWatcher, err := funcs.newWatcher()
	if err != nil {
		return nil, err
	}
	if err = funcs.addWatcherPath(dirWatcher, path); err != nil {
		_ = dirWatcher.Close()
		return nil, err
	}
	wk := &worker{
		dirWatcher:      dirWatcher,
		watchedFiles:    make(map[string]*fileTracker),
		retireTrackerCh: make(chan *fileTracker),
		terminateCh:     make(chan bool),
	}
	go wk.listen()
	return wk, nil
}

func (wk *worker) listen() {
	wk.loop()
	_ = wk.dirWatcher.Close()
	wk.drainRetiringTrackers()
	for _, ft := range wk.watchedFiles {
		retireTracker(ft)
	}
}

func (wk *worker) loop() {
	for {
		select {
		case event := <-wk.dirWatcher.Events:
			for path, ft := range wk.getTrackers() {
				if ft.events == nil {
					continue
				}
				sum, isDir := getHashSum(path)
				if isDir || !bytes.Equal(sum, ft.hash) {
					ft.hash = sum
					select {
					case ft.events <- event:
					case ft := <-wk.retireTrackerCh:
						retireTracker(ft)
					case <-wk.terminateCh:
						return
					}
				}
			}

		case err := <-wk.dirWatcher.Errors:
			for _, ft := range wk.getTrackers() {
				if ft.errors == nil {
					continue
				}
				select {
				case ft.errors <- err:
				case ft := <-wk.retireTrackerCh:
					retireTracker(ft)
				case <-wk.terminateCh:
					return
				}
			}

		case ft := <-wk.retireTrackerCh:
			retireTracker(ft)

		case <-wk.terminateCh:
			return
		}
	}
}

func (wk *worker) drainRetiringTrackers() {
	for {
		select {
		case ft := <-wk.retireTrackerCh:
			retireTracker(ft)
		default:
			return
		}
	}
}

func (wk *worker) getTrackers() map[string]*fileTracker {
	wk.mu.RLock()
	result := make(map[string]*fileTracker, len(wk.watchedFiles))
	for k, v := range wk.watchedFiles {
		result[k] = v
	}
	wk.mu.RUnlock()
	return result
}

func retireTracker(ft *fileTracker) {
	close(ft.events)
	close(ft.errors)
	ft.events = nil
	ft.errors = nil
}

func (wk *worker) terminate() {
	wk.terminateCh <- true
}

func (wk *worker) addPath(path string) error {
	wk.mu.Lock()
	defer wk.mu.Unlock()

	if wk.watchedFiles[path] != nil {
		return fmt.Errorf("path %s is already being watched", path)
	}
	h, _ := getHashSum(path)
	wk.watchedFiles[path] = &fileTracker{
		events: make(chan fsnotify.Event),
		errors: make(chan error),
		hash:   h,
	}
	return nil
}

func (wk *worker) removePath(path string) error {
	wk.mu.Lock()
	defer wk.mu.Unlock()

	ft := wk.watchedFiles[path]
	if ft == nil {
		return fmt.Errorf("path %s not found", path)
	}
	delete(wk.watchedFiles, path)
	wk.retireTrackerCh <- ft
	return nil
}

func (wk *worker) eventChannel(path string) chan fsnotify.Event {
	wk.mu.RLock()
	defer wk.mu.RUnlock()
	if ft := wk.watchedFiles[path]; ft != nil {
		return ft.events
	}
	return nil
}

func (wk *worker) errorChannel(path string) chan error {
	wk.mu.RLock()
	defer wk.mu.RUnlock()
	if ft := wk.watchedFiles[path]; ft != nil {
		return ft.errors
	}
	return nil
}

// getHashSum returns the SHA-256 hash of a file, or nil if unreadable or a directory.
func getHashSum(file string) ([]byte, bool) {
	f, err := os.Open(file)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	fi, err := f.Stat()
	if err != nil {
		return nil, false
	}
	if fi.IsDir() {
		return nil, true
	}

	h := sha256.New()
	_, _ = io.Copy(h, bufio.NewReader(f))
	return h.Sum(nil), false
}
