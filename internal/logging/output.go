package logging

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"hashinex/internal/config"
)

const logDate = "2006-01-02"

type record struct {
	data     []byte
	queuedAt time.Time
}

type Output struct {
	mu         sync.Mutex
	console    io.Writer
	dir        string
	prefix     string
	ext        string
	file       *os.File
	day        string
	recent     [256][]byte
	position   int
	queue      chan record
	stop       chan struct{}
	done       chan struct{}
	dropped    atomic.Uint64
	closed     bool
	keep       atomic.Int64
	keepChange chan struct{}
	prunedKeep int
}

func Open(console io.Writer, cfg config.Logging) (*Output, error) {
	o := &Output{console: console, queue: make(chan record, 512),
		stop: make(chan struct{}), done: make(chan struct{}), keepChange: make(chan struct{}, 1)}
	o.keep.Store(int64(cfg.Keep))
	if cfg.File != "" {
		o.dir = filepath.Dir(cfg.File)
		name := filepath.Base(cfg.File)
		o.ext = filepath.Ext(name)
		o.prefix = strings.TrimSuffix(name, o.ext) + "-"
		if err := os.MkdirAll(o.dir, 0700); err != nil {
			return nil, err
		}
		if err := o.openDay(time.Now().Format(logDate)); err != nil {
			return nil, err
		}
	}
	go o.run()
	return o, nil
}

func (o *Output) openDay(day string) error {
	file, err := os.OpenFile(filepath.Join(o.dir, o.prefix+day+o.ext), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if o.file != nil {
		_ = o.file.Close()
	}
	o.file, o.day, o.prunedKeep = file, day, 0
	return nil
}

func (o *Output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return len(p), nil
	}
	select {
	case o.queue <- record{data: bytes.Clone(p), queuedAt: time.Now()}:
	default:
		o.dropped.Add(1)
	}
	return len(p), nil
}

func (o *Output) run() {
	defer close(o.done)
	for {
		select {
		case record := <-o.queue:
			o.write(record)
		case <-o.keepChange:
			o.applyKeep()
		case <-o.stop:
			for {
				select {
				case record := <-o.queue:
					o.write(record)
				default:
					return
				}
			}
		}
	}
}

func (o *Output) write(entry record) {
	if dropped := o.dropped.Swap(0); dropped != 0 {
		_, _ = fmt.Fprintln(o.console, "Hashinex log queue overflow; records dropped:", dropped)
	}
	p := entry.data
	o.mu.Lock()
	o.recent[o.position] = bytes.TrimSpace(p)
	o.position = (o.position + 1) % len(o.recent)
	o.mu.Unlock()
	_, _ = o.console.Write(p)
	if o.file == nil {
		return
	}
	if day := entry.queuedAt.Format(logDate); day != o.day {
		if err := o.openDay(day); err != nil {
			_, _ = fmt.Fprintln(o.console, "Hashinex daily log open failed:", err)
			return
		}
	}
	if _, err := o.file.Write(p); err != nil {
		_, _ = fmt.Fprintln(o.console, "Hashinex file log write failed:", err)
	}
	o.applyKeep()
}

func (o *Output) applyKeep() {
	keep := int(o.keep.Load())
	if o.file == nil || keep < 1 || keep == o.prunedKeep {
		return
	}
	o.prunedKeep = keep
	if err := o.prune(keep); err != nil {
		_, _ = fmt.Fprintln(o.console, "Hashinex daily log cleanup failed:", err)
	}
}

func (o *Output) prune(keep int) error {
	files, err := os.ReadDir(o.dir)
	if err != nil {
		return err
	}
	// ReadDir sorts by name, so the YYYY-MM-DD suffix orders daily files by date.
	// Reserve one slot for the open file even if the system clock moved backwards.
	remaining := keep - 1
	for i := len(files) - 1; i >= 0; i-- {
		file := files[i]
		if !file.Type().IsRegular() {
			continue
		}
		suffix, matchesPrefix := strings.CutPrefix(file.Name(), o.prefix)
		day, matchesExt := strings.CutSuffix(suffix, o.ext)
		if !matchesPrefix || !matchesExt || len(day) != len(logDate) || day == o.day {
			continue
		}
		if _, parseErr := time.Parse(logDate, day); parseErr != nil {
			continue
		}
		if remaining > 0 {
			remaining--
			continue
		}
		if removeErr := os.Remove(filepath.Join(o.dir, file.Name())); removeErr != nil && !os.IsNotExist(removeErr) {
			err = errors.Join(err, removeErr)
		}
	}
	return err
}

func (o *Output) SetKeep(keep int) {
	o.keep.Store(int64(keep))
	select {
	case o.keepChange <- struct{}{}:
	default:
	}
}

func (o *Output) Recent() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	rows := make([]string, 0, len(o.recent))
	for i := range o.recent {
		if data := o.recent[(o.position+i)%len(o.recent)]; len(data) > 0 {
			rows = append(rows, string(data))
		}
	}
	return rows
}

func (o *Output) Close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	close(o.stop)
	o.mu.Unlock()
	<-o.done
	if o.file != nil {
		err := o.file.Close()
		o.file = nil
		return err
	}
	return nil
}
