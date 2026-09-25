package config

import (
	"reflect"
	"time"
)

// Watch loads a file-based config of type T (via FromFile), then polls it on
// interval, calling onChange whenever the freshly-parsed value differs from
// the last one it saw (compared with reflect.DeepEqual).
//
// This is deliberately simple stdlib polling, not an fsnotify integration: it
// costs one file read and parse per tick, which is cheap enough for any
// config file and needs no new dependency. Call the returned stop function to
// end the poll loop; it is safe to call once and only once.
//
// Extra sources may be supplied and are layered under the file the same way
// Load layers them, in case some fields should keep coming from the
// environment even while the file is hot-reloaded; the file itself is always
// applied last (highest precedence) among the sources passed to Watch.
func Watch[T any](path string, interval time.Duration, onChange func(T), sources ...Source) (stop func(), err error) {
	load := func() (T, error) {
		all := append(append([]Source{}, sources...), FromFile(path))
		return Load[T](all...)
	}

	prev, err := load()
	if err != nil {
		return nil, err
	}

	stopCh := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				cur, loadErr := load()
				if loadErr != nil {
					// A transient parse error (e.g. a half-written file)
					// should not tear down the watch; just try again next
					// tick.
					continue
				}
				if !reflect.DeepEqual(prev, cur) {
					prev = cur
					if onChange != nil {
						onChange(cur)
					}
				}
			}
		}
	}()

	stop = func() {
		close(stopCh)
		<-done
	}
	return stop, nil
}
