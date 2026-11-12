package matching

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"math/bits"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"hashinex/internal/qqofficial"
)

type fingerprint struct {
	sha    [32]byte
	phash  uint64
	usable bool
}

type avatar struct {
	hash    fingerprint
	qq      int64
	expires time.Time
}

type AvatarVerifier struct {
	client   *http.Client
	mu       sync.Mutex
	items    map[string]avatar
	inflight map[string]chan struct{}
	defaults []uint64
}

func NewAvatarVerifier(defaults []string) *AvatarVerifier {
	a := &AvatarVerifier{
		items: make(map[string]avatar), inflight: make(map[string]chan struct{}),
		client: &http.Client{
			Timeout: 3 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 || !safeAvatarHost(req.URL) {
					return errors.New("avatar redirect rejected")
				}
				return nil
			},
		},
	}
	for _, value := range defaults {
		hash, _ := strconv.ParseUint(value, 16, 64)
		a.defaults = append(a.defaults, hash)
	}
	return a
}

func safeAvatarHost(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && (u.Port() == "" || u.Port() == "443") &&
		slices.Contains([]string{"q.qlogo.cn", "q1.qlogo.cn", "thirdqq.qlogo.cn"}, u.Hostname())
}

func (a *AvatarVerifier) Verify(ctx context.Context, appID, openID string, qq int64, maxDistance int) (int, bool) {
	official := qqofficial.AvatarURL(appID, openID)
	observer := "https://q.qlogo.cn/g?b=qq&nk=" + strconv.FormatInt(qq, 10) + "&s=100"
	left := a.get(ctx, official, 0)
	if !left.usable {
		return 0, false
	}
	right := a.get(ctx, observer, qq)
	if !right.usable {
		return 0, false
	}
	distance := bits.OnesCount64(left.phash ^ right.phash)
	if left.sha != right.sha && distance > maxDistance {
		return distance, false
	}
	for _, hash := range a.defaults {
		if bits.OnesCount64(hash^left.phash) <= maxDistance || bits.OnesCount64(hash^right.phash) <= maxDistance {
			return distance, false
		}
	}
	now := time.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, item := range a.items {
		if item.qq != 0 && item.qq != qq && now.Before(item.expires) && item.hash.usable &&
			bits.OnesCount64(item.hash.phash^right.phash) <= maxDistance {
			return distance, false
		}
	}
	return distance, true
}

func (a *AvatarVerifier) get(ctx context.Context, address string, qq int64) fingerprint {
	a.mu.Lock()
	if item, ok := a.items[address]; ok && time.Now().Before(item.expires) {
		a.mu.Unlock()
		return item.hash
	}
	if wait := a.inflight[address]; wait != nil {
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return fingerprint{}
		case <-wait:
			a.mu.Lock()
			item := a.items[address]
			a.mu.Unlock()
			return item.hash
		}
	}
	done := make(chan struct{})
	a.inflight[address] = done
	a.mu.Unlock()
	var hash fingerprint
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.items) >= 1024 {
			var oldestKey string
			var oldest time.Time
			for key, item := range a.items {
				if oldest.IsZero() || item.expires.Before(oldest) {
					oldest, oldestKey = item.expires, key
				}
			}
			delete(a.items, oldestKey)
		}
		ttl := 2 * time.Minute
		if !hash.usable {
			ttl = 10 * time.Second
		}
		a.items[address] = avatar{hash, qq, time.Now().Add(ttl)}
		delete(a.inflight, address)
		close(done)
	}()
	hash = a.fetch(ctx, address)
	return hash
}

func (a *AvatarVerifier) fetch(ctx context.Context, address string) fingerprint {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil || !safeAvatarHost(req.URL) {
		return fingerprint{}
	}
	req.Header.Set("User-Agent", "Hashinex/1.0")
	req.Header.Set("Accept", "image/jpeg,image/png,image/gif")
	resp, err := a.client.Do(req)
	if err != nil {
		return fingerprint{}
	}
	defer resp.Body.Close()
	const limit = 2 << 20
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return fingerprint{}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || len(body) > limit {
		return fingerprint{}
	}
	return hashImage(body)
}

var cosine = func() [8][32]float64 {
	var values [8][32]float64
	for f := range values {
		for i := range values[f] {
			values[f][i] = math.Cos(math.Pi * float64((2*i+1)*f) / 64)
		}
	}
	return values
}()

func hashImage(body []byte) fingerprint {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width < 24 || cfg.Height < 24 || cfg.Width > 1024 || cfg.Height > 1024 {
		return fingerprint{}
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return fingerprint{}
	}
	b := img.Bounds()
	var gray [32][32]float64
	var sum, squared float64
	for y := range gray {
		for x := range gray[y] {
			r, g, b, a := img.At(b.Min.X+(2*x+1)*b.Dx()/64, b.Min.Y+(2*y+1)*b.Dy()/64).RGBA()
			luma := (0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b) + float64(65535-a)) / 257
			gray[y][x] = luma
			sum += luma
			squared += luma * luma
		}
	}
	if squared/1024-math.Pow(sum/1024, 2) < 64 {
		return fingerprint{}
	}
	var rowDCT [32][8]float64
	for y := range rowDCT {
		for f := range rowDCT[y] {
			for x, value := range gray[y] {
				rowDCT[y][f] += value * cosine[f][x]
			}
		}
	}
	var coeff [64]float64
	for fy := range 8 {
		for fx := range 8 {
			for y := range rowDCT {
				coeff[fy*8+fx] += rowDCT[y][fx] * cosine[fy][y]
			}
		}
	}
	sorted := coeff
	slices.Sort(sorted[1:])
	median := sorted[32]
	var phash uint64
	for i := 1; i < len(coeff); i++ {
		if coeff[i] > median {
			phash |= 1 << i
		}
	}
	return fingerprint{sha: sha256.Sum256(body), phash: phash, usable: true}
}
