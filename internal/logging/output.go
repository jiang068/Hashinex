package logging

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"

	"hashinex/internal/config"
)

type Output struct {
	mu       sync.Mutex
	console  io.Writer
	cfg      config.Logging
	file     *os.File
	size     int64
	recent   [256][]byte
	position int
	queue    chan []byte
	stop     chan struct{}
	done     chan struct{}
	dropped  atomic.Uint64
	closed   atomic.Bool
}

func Open(console io.Writer, cfg config.Logging) (*Output, error) {
	o := &Output{console: console, cfg: cfg, queue: make(chan []byte, 512),
		stop: make(chan struct{}), done: make(chan struct{})}
	if cfg.File != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0700); err != nil {
			return nil, err
		}
		if err := o.openFile(); err != nil {
			return nil, err
		}
	}
	go o.run()
	return o, nil
}

func (o *Output) openFile() error {
	file, err := os.OpenFile(o.cfg.File, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	o.file, o.size = file, info.Size()
	return nil
}

func (o *Output) Write(p []byte) (int, error) {
	if o.closed.Load() {
		return len(p), nil
	}
	select {
	case o.queue <- bytes.Clone(p):
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

func (o *Output) write(p []byte) {
	if dropped := o.dropped.Swap(0); dropped != 0 {
		_, _ = fmt.Fprintln(o.console, "Hashinex log queue overflow; records dropped:", dropped)
	}
	o.mu.Lock()
	o.recent[o.position] = bytes.TrimSpace(p)
	o.position = (o.position + 1) % len(o.recent)
	maxSize, keep := o.cfg.MaxSize, o.cfg.Keep
	o.mu.Unlock()
	_, _ = o.console.Write(p)
	if o.file == nil {
		return
	}
	if o.size > 0 && o.size+int64(len(p)) > maxSize {
		if err := o.rotate(keep); err != nil {
			_, _ = fmt.Fprintln(o.console, "Hashinex file log rotation failed:", err)
			return
		}
	}
	written, err := o.file.Write(p)
	o.size += int64(written)
	if err != nil {
		_, _ = fmt.Fprintln(o.console, "Hashinex file log write failed:", err)
	}
}

func (o *Output) rotate(keep int) error {
	o.file.Close()
	o.file = nil
	for i := keep - 1; i >= 0; i-- {
		source := o.cfg.File
		if i != 0 {
			source += "." + strconv.Itoa(i)
		}
		target := o.cfg.File + "." + strconv.Itoa(i+1)
		if err := os.Rename(source, target); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return o.openFile()
}

func (o *Output) SetLimits(maxSize int64, keep int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.cfg.MaxSize, o.cfg.Keep = maxSize, keep
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
	if !o.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(o.stop)
	<-o.done
	if o.file != nil {
		err := o.file.Close()
		o.file = nil
		return err
	}
	return nil
}
