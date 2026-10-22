package main

// Minimal inotify wrapper (Linux) used by the storage analyzer to keep its
// index current without rescanning.

import (
	"context"
	"errors"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

const watchMask = syscall.IN_CREATE | syscall.IN_DELETE | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_CLOSE_WRITE | syscall.IN_ATTRIB | syscall.IN_DELETE_SELF | syscall.IN_ONLYDIR | syscall.IN_DONT_FOLLOW

type inotify struct {
	fd     int
	f      *os.File
	mu     sync.Mutex
	wds    map[int32]string // watch descriptor -> folder (root-relative)
	byPath map[string]int32
	full   bool // hit the kernel watch limit
}

func newInotify() (*inotify, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	return &inotify{fd: fd, f: os.NewFile(uintptr(fd), "inotify"), wds: map[int32]string{}, byPath: map[string]int32{}}, nil
}

func (in *inotify) add(abs, rel string) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.full {
		return syscall.ENOSPC
	}
	if _, ok := in.byPath[rel]; ok {
		return nil
	}
	wd, err := syscall.InotifyAddWatch(in.fd, abs, watchMask)
	if err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			in.full = true
		}
		return err
	}
	in.wds[int32(wd)] = rel
	in.byPath[rel] = int32(wd)
	return nil
}

func (in *inotify) forget(wd int32) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if rel, ok := in.wds[wd]; ok {
		delete(in.byPath, rel)
		delete(in.wds, wd)
	}
}

// renamePrefix keeps watch paths correct after a folder is moved.
func (in *inotify) renamePrefix(from, to string) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for wd, rel := range in.wds {
		if rel == from || strings.HasPrefix(rel, from+"/") {
			nr := to + strings.TrimPrefix(rel, from)
			delete(in.byPath, rel)
			in.wds[wd] = nr
			in.byPath[nr] = wd
		}
	}
}

func (in *inotify) Close() error { return in.f.Close() }

// watchTree adds watches for n and all folders below it.
func (a *Analyzer) watchTree(n *aNode, rel string) {
	if a.watch == nil {
		return
	}
	if err := a.watch.add(a.abs(rel), rel); err != nil {
		if errors.Is(err, syscall.ENOSPC) {
			return
		}
	}
	for _, d := range n.Dirs {
		a.watchTree(d, relJoin(rel, d.Name))
	}
}

// rewatch (re)registers watches for every folder after a scan.
func (a *Analyzer) rewatch() {
	if a.watch == nil {
		return
	}
	if !a.rewatching.CompareAndSwap(false, true) {
		return // another rewatch is already registering folders
	}
	defer a.rewatching.Store(false)
	// snapshot folder paths under the read lock, add watches outside it
	var paths []string
	a.mu.RLock()
	var walk func(n *aNode, rel string)
	walk = func(n *aNode, rel string) {
		paths = append(paths, rel)
		for _, d := range n.Dirs {
			walk(d, relJoin(rel, d.Name))
		}
	}
	if a.tree != nil {
		walk(a.tree, "")
	}
	a.mu.RUnlock()
	for _, rel := range paths {
		if err := a.watch.add(a.abs(rel), rel); errors.Is(err, syscall.ENOSPC) {
			break
		}
	}
	a.watch.mu.Lock()
	full, n := a.watch.full, len(a.watch.wds)
	a.watch.mu.Unlock()
	if full && !a.limitLogged.Swap(true) {
		log.Printf("analyzer: inotify watch limit reached after %d folders; raise fs.inotify.max_user_watches for live updates. Falling back to a quick check every 10 minutes.", n)
	}
	a.watchAll.Store(!full)
}

func (a *Analyzer) watchLoop(ctx context.Context) {
	buf := make([]byte, 64<<10)
	var moveCookie uint32
	var moveFrom string
	for ctx.Err() == nil {
		n, err := a.watch.f.Read(buf)
		if err != nil {
			return
		}
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameBytes := buf[off+syscall.SizeofInotifyEvent : off+syscall.SizeofInotifyEvent+int(ev.Len)]
			name := strings.TrimRight(string(nameBytes), "\x00")
			off += syscall.SizeofInotifyEvent + int(ev.Len)

			if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
				go func() { _ = a.Refresh(ctx) }()
				continue
			}
			a.watch.mu.Lock()
			rel, ok := a.watch.wds[ev.Wd]
			a.watch.mu.Unlock()
			if !ok {
				continue
			}
			if ev.Mask&syscall.IN_IGNORED != 0 || ev.Mask&syscall.IN_DELETE_SELF != 0 {
				a.watch.forget(ev.Wd)
				continue
			}
			isDir := ev.Mask&syscall.IN_ISDIR != 0
			if isDir && ev.Mask&syscall.IN_MOVED_FROM != 0 {
				moveCookie, moveFrom = ev.Cookie, relJoin(rel, name)
			}
			if isDir && ev.Mask&syscall.IN_MOVED_TO != 0 && ev.Cookie == moveCookie && moveFrom != "" {
				a.watch.renamePrefix(moveFrom, relJoin(rel, name))
				moveFrom = ""
			}
			a.markDirty(rel)
		}
	}
}
