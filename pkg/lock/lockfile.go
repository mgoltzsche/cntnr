package lock

import (
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/nightlyone/lockfile"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type Lockfile struct {
	file     string
	lockfile lockfile.Lockfile
}

func LockFile(file string) (*Lockfile, error) {
	file = filepath.Clean(file)
	l, err := lockfile.New(file)
	return &Lockfile{file, l}, err
}

func (l *Lockfile) TryLock() (err error) {
	lock(l.file)

	defer func() {
		if err != nil {
			err = errors.Wrap(err, "trylock")
			unlock(l.file)
		}
	}()

	if err = l.mkdirs(); err != nil {
		return
	}

	return l.lockfile.TryLock()
}

func (l *Lockfile) mkdirs() error {
	return errors.Wrap(os.MkdirAll(filepath.Dir(l.file), 0755), "mk lock parent dir")
}

func (l *Lockfile) Lock() (err error) {
	lock(l.file)

	defer func() {
		if err != nil {
			err = errors.Wrap(err, "lock")
			unlock(l.file)
		}
	}()

	if err = l.mkdirs(); err != nil {
		return
	}

	for {
		err = l.lockfile.TryLock()
		if terr, ok := err.(lockfile.TemporaryError); err == nil || !ok || !terr.Temporary() {
			// return when locked successfully or error is not temporary
			return
		}
		if err = awaitFileChange(l.file); err != nil && !os.IsNotExist(err) {
			return
		}
	}
	return
}

func (l *Lockfile) Unlock() (err error) {
	defer unlock(l.file)
	if err = l.lockfile.Unlock(); err != nil {
		err = errors.New("unlock: " + err.Error())
	}
	return
}

func normalizePath(path string) (f string, err error) {
	if f, err = filepath.EvalSymlinks(path); err != nil {
		if os.IsNotExist(err) {
			f, err = normalizePath(filepath.Dir(path))
			f = filepath.Join(f, filepath.Base(path))
			if err != nil {
				return
			}
		}
	}
	if err == nil {
		f, err = filepath.Abs(f)
	}
	if err != nil {
		err = errors.Errorf("normalize path %q: %s", path, err)
	}
	return
}

func awaitFileChange(files ...string) (err error) {
	if len(files) == 0 {
		panic("No files provided to watch")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return errors.New(err.Error())
	}
	defer watcher.Close()
	for _, file := range files {
		if err = watcher.Add(file); err != nil {
			return errors.New(err.Error())
		}
	}
	log := logrus.WithField("files", files)
	timer := time.NewTimer(5 * time.Second)
	select {
	case event := <-watcher.Events:
		log.Debugln("watch lockfile:", event)
		return
	case err = <-watcher.Errors:
		log.Debugln("watch lockfile:", err)
		return
	case <-timer.C:
		// Timeout to prevent deadlock after other process dies without deleting its lockfile
		log.Debugln("lockfile watch time expired")
		return
	}
}
