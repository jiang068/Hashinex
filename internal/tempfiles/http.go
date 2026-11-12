package tempfiles

import (
	"context"
	"html/template"
	"mime"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Service struct {
	store    *Store
	public   string
	prefix   string
	ttl      time.Duration
	maxBytes int64
	clients  chan struct{}
}

func NewService(store *Store, public string, ttl time.Duration, maxBytes int64) *Service {
	address, _ := url.Parse(public) // Validated by the gateway configuration.
	return &Service{store: store, public: public, prefix: address.Path + "/", ttl: ttl, maxBytes: maxBytes, clients: make(chan struct{}, 24)}
}

func (s *Service) Publish(ctx context.Context, parts []Part) (string, error) {
	id, err := s.store.publish(ctx, parts, s.ttl, s.maxBytes)
	if err != nil {
		return "", err
	}
	return s.public + "/" + id, nil
}

func WebURL(value string) bool {
	if len(value) > 8192 {
		return false
	}
	u, err := url.Parse(value)
	if err != nil || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip, err := netip.ParseAddr(host); err == nil {
		ip = ip.Unmap()
		return ip.IsGlobalUnicast() && !ip.IsPrivate()
	}
	dot := strings.LastIndexByte(host, '.')
	return dot > 0 && strings.IndexFunc(host[dot+1:], func(r rune) bool { return r >= 'a' && r <= 'z' }) != -1 &&
		!strings.HasSuffix(host, ".localhost") &&
		!strings.HasSuffix(host, ".local") && !strings.HasSuffix(host, ".internal") &&
		!strings.ContainsAny(host, ":[]")
}

func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src 'self' https:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; sandbox allow-downloads")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	path, ok := strings.CutPrefix(r.URL.EscapedPath(), s.prefix)
	id, asset, hasAsset := strings.Cut(path, "/")
	if !ok || !validID(id) {
		http.NotFound(w, r)
		return
	}
	index := 0
	if hasAsset {
		index, _ = strconv.Atoi(asset)
		if index < 1 || index > MaxParts || strconv.Itoa(index) != asset {
			http.NotFound(w, r)
			return
		}
	}
	select {
	case s.clients <- struct{}{}:
		defer func() { <-s.clients }()
	default:
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	s.store.mu.Lock()
	p := s.store.pages[id]
	now := time.Now()
	if p == nil || !now.Before(p.Expires) {
		s.store.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	p.readers++
	expires := p.Expires
	s.store.mu.Unlock()
	defer func() {
		s.store.mu.Lock()
		p.readers--
		s.store.mu.Unlock()
	}()
	if index > 0 {
		s.serveAsset(w, r, id, p.Parts, index)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	parts := make([]Part, len(p.Parts))
	copy(parts, p.Parts)
	for i := range parts {
		if parts[i].Asset > 0 {
			parts[i].URL = s.prefix + id + "/" + strconv.Itoa(parts[i].Asset)
		}
	}
	_ = sharePage.Execute(w, struct {
		Parts   []Part
		Expires string
	}{parts, expires.UTC().Format("2006-01-02 15:04:05 UTC")})
}

func (s *Service) serveAsset(w http.ResponseWriter, r *http.Request, id string, parts []Part, index int) {
	if index > len(parts) || parts[index-1].Asset != index {
		http.NotFound(w, r)
		return
	}
	part := parts[index-1]
	file, err := s.store.root.Open(assetPath(id, index))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	if part.Kind == "image" && inlineImage(part.MIME) {
		w.Header().Set("Content-Type", part.MIME)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": part.Name}))
	}
	http.ServeContent(w, r, "", time.Time{}, file)
}

var sharePage = template.Must(template.New("share").Parse(`<!doctype html>
<html lang="zh-CN">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>临时消息 · Hashinex</title>
<style>
  :root{color-scheme:light dark;font-family:Inter,system-ui,sans-serif}
  body{margin:0;background:light-dark(#f4f5f7,#121519);color:light-dark(#161b22,#eff2f5)}
  main{max-width:50rem;margin:0 auto;padding:2rem 1rem 4rem}
  header{border-bottom:1px solid light-dark(#d8dee8,#363d46);margin-bottom:1.25rem;padding-bottom:1rem}
  h1{font-size:1.55rem;margin:0 0 .4rem}
  small{color:light-dark(#49576b,#aab7c7)}
  .text{white-space:pre-wrap;overflow-wrap:anywhere;line-height:1.6;margin:.75rem 0}
  figure{margin:1rem 0}
  img{display:block;max-width:100%;height:auto;border-radius:.4rem}
  h2{font-size:1rem;border-top:1px solid light-dark(#d8dee8,#363d46);padding-top:.8rem;margin:1.25rem 0 .5rem}
  a{color:light-dark(#194a9b,#a0c9ff);overflow-wrap:anywhere}
  .file{display:inline-block;margin:.5rem 0;padding:.65rem .8rem;background:light-dark(#e9eef7,#212c3a);border-radius:.35rem}
</style>
<main>
<header><h1>临时消息</h1><small>有效至 {{.Expires}}</small></header>
{{range .Parts}}
{{if eq .Kind "text"}}<div class="text">{{.Text}}</div>
{{else if eq .Kind "heading"}}<h2>{{.Text}}</h2>
{{else if eq .Kind "image"}}<figure><img src="{{.URL}}" alt="消息图片" loading="lazy" referrerpolicy="no-referrer">{{if eq .Asset 0}}<a href="{{.URL}}" rel="noreferrer noopener">打开原图</a>{{end}}</figure>
{{else if eq .Kind "file"}}<div><a class="file" href="{{.URL}}" rel="noreferrer noopener">{{.Name}}</a></div>
{{end}}
{{end}}
</main>
</html>`))
