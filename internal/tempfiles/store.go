package tempfiles

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	MaxParts      = 256
	maxShares     = 4096
	maxManifest   = 512 << 10
	shareOverhead = 4096
)

var (
	ErrCapacity = errors.New("temporary share capacity exceeded")
	ErrAsset    = errors.New("invalid temporary share asset")
)

type Source struct {
	Size   int64
	Reader io.Reader
}

type Part struct {
	Kind   string  `json:"kind"`
	Text   string  `json:"text,omitempty"`
	Name   string  `json:"name,omitempty"`
	URL    string  `json:"url,omitempty"`
	Asset  int     `json:"asset,omitempty"`
	MIME   string  `json:"mime,omitempty"`
	Source *Source `json:"-"`
}

type page struct {
	Expires time.Time `json:"expires"`
	Bytes   int64     `json:"bytes"`
	Parts   []Part    `json:"parts"`
	readers int
}

type Store struct {
	root  *os.Root
	mu    sync.Mutex
	pages map[string]*page
	bytes int64
	busy  int
	stop  chan struct{}
	done  chan struct{}
}

func Open(directory string) (*Store, error) {
	directory = filepath.Join(directory, "hashinex-shares")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root, pages: make(map[string]*page), stop: make(chan struct{}), done: make(chan struct{})}
	if err := s.restore(); err != nil {
		_ = root.Close()
		return nil, err
	}
	go s.clean()
	return s, nil
}

func (s *Store) restore() error {
	directory, err := s.root.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	for {
		entries, readErr := directory.ReadDir(128)
		for _, entry := range entries {
			id := entry.Name()
			if !entry.IsDir() || !validID(id) {
				continue
			}
			p := s.readPage(id)
			if p == nil || !time.Now().Before(p.Expires) {
				if err := s.root.RemoveAll(id); err != nil {
					return err
				}
				continue
			}
			if len(s.pages) >= maxShares || p.Bytes > (1<<52)-s.bytes {
				return errors.New("temporary share storage contains too many entries")
			}
			s.pages[id] = p
			s.bytes += p.Bytes
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (s *Store) readPage(id string) *page {
	f, err := s.root.Open(filepath.Join(id, "page.json"))
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxManifest+1))
	var p page
	if err != nil || len(data) > maxManifest || json.Unmarshal(data, &p) != nil ||
		len(p.Parts) == 0 || len(p.Parts) > MaxParts || p.Bytes < shareOverhead {
		return nil
	}
	return &p
}

func (s *Store) Close() error {
	close(s.stop)
	<-s.done
	return s.root.Close()
}

func (s *Store) clean() {
	defer close(s.done)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			s.expireLocked(now)
			s.mu.Unlock()
		}
	}
}

func (s *Store) expireLocked(now time.Time) {
	for id, p := range s.pages {
		if p.readers == 0 && !now.Before(p.Expires) {
			s.removeLocked(id, p)
		}
	}
}

func (s *Store) removeLocked(id string, p *page) {
	if s.root.RemoveAll(id) == nil {
		delete(s.pages, id)
		s.bytes -= p.Bytes
	}
}

func (s *Store) publish(ctx context.Context, parts []Part, ttl time.Duration, maxBytes int64) (string, error) {
	if len(parts) == 0 || len(parts) > MaxParts {
		return "", ErrAsset
	}
	meta, err := json.Marshal(parts)
	if err != nil || len(meta) > maxManifest-shareOverhead-MaxParts*64 {
		return "", ErrAsset
	}
	charged := int64(len(meta) + shareOverhead + len(parts)*64)
	for _, part := range parts {
		if part.Source == nil {
			continue
		}
		if part.Source.Size <= 0 {
			return "", ErrAsset
		}
		if part.Source.Size > maxBytes-charged {
			return "", ErrCapacity
		}
		charged += part.Source.Size
	}
	s.mu.Lock()
	if s.bytes > maxBytes-charged || len(s.pages)+s.busy >= maxShares {
		s.expireLocked(time.Now())
	}
	if s.bytes > maxBytes-charged || len(s.pages)+s.busy >= maxShares {
		s.mu.Unlock()
		return "", ErrCapacity
	}
	s.bytes += charged
	s.busy++
	s.mu.Unlock()
	var id string
	var p *page
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.busy--
		switch {
		case p != nil:
			s.pages[id] = p
		case id != "" && s.root.RemoveAll(id) != nil:
			s.pages[id] = &page{Bytes: charged}
		default:
			s.bytes -= charged
		}
	}()
	id, err = s.newDirectory()
	if err != nil {
		return "", err
	}
	for i := range parts {
		part := &parts[i]
		if part.Source == nil {
			continue
		}
		part.Asset = i + 1
		part.MIME, err = s.writeAsset(ctx, id, *part)
		if err != nil {
			return "", err
		}
		part.Source = nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	created := &page{Expires: time.Now().Add(ttl), Bytes: charged, Parts: parts}
	raw, err := json.Marshal(created)
	if err != nil {
		return "", err
	}
	if err := s.root.WriteFile(filepath.Join(id, "page.json"), raw, 0600); err != nil {
		return "", err
	}
	p = created
	return id, nil
}

func (s *Store) newDirectory() (string, error) {
	for {
		var random [6]byte
		if _, err := rand.Read(random[:]); err != nil {
			return "", err
		}
		id := base64.RawURLEncoding.EncodeToString(random[:])
		if strings.ContainsAny(id, "-_") {
			continue
		}
		if err := s.root.Mkdir(id, 0700); !errors.Is(err, os.ErrExist) {
			if err != nil {
				return "", err
			}
			return id, nil
		}
	}
}

func validID(id string) bool {
	switch len(id) {
	case 8:
		return strings.IndexFunc(id, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		}) == -1
	case 39:
		// Existing shares keep their original numeric IDs until expiration.
		return strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) == -1
	default:
		return false
	}
}

func (s *Store) writeAsset(ctx context.Context, id string, part Part) (string, error) {
	in := &checkedReader{ctx: ctx, input: part.Source.Reader}
	prefix := make([]byte, min(512, part.Source.Size))
	if _, err := io.ReadFull(in, prefix); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ErrAsset
	}
	mime := "application/octet-stream"
	if part.Kind == "image" {
		mime = http.DetectContentType(prefix)
		if !inlineImage(mime) {
			return "", ErrAsset
		}
	}
	out, err := s.root.OpenFile(assetPath(id, part.Asset), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, headErr := out.Write(prefix)
	remaining := part.Source.Size - int64(len(prefix))
	_, copyErr := io.CopyN(out, in, remaining)
	var extra [1]byte
	if copyErr == nil {
		_, readErr := io.ReadFull(in, extra[:])
		switch {
		case readErr == nil:
			copyErr = ErrAsset
		case !errors.Is(readErr, io.EOF):
			copyErr = readErr
		}
	}
	closeErr := out.Close()
	var corrupt base64.CorruptInputError
	if errors.Is(copyErr, io.EOF) || errors.As(copyErr, &corrupt) {
		copyErr = ErrAsset
	}
	if err := errors.Join(headErr, copyErr, closeErr); err != nil {
		return "", err
	}
	return mime, nil
}

func assetPath(id string, asset int) string {
	return filepath.Join(id, strconv.Itoa(asset))
}

func inlineImage(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp", "image/bmp", "image/x-icon":
		return true
	default:
		return false
	}
}

type checkedReader struct {
	ctx   context.Context
	input io.Reader
}

func (r *checkedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.input.Read(p)
}
