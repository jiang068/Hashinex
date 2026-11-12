package qqofficial

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const apiBase = "https://api.bot.qq.com"

type Client struct {
	appID             string
	secret            string
	base              string
	http              *http.Client
	webhookPrivateKey ed25519.PrivateKey
	webhookPublicKey  ed25519.PublicKey
	tokenMu           sync.Mutex
	token             string
	refresh           time.Time
}

func NewClient(appID, secret, origin string, timeout time.Duration) *Client {
	base := apiBase
	if origin == "sgroup" {
		base = "https://api.sgroup.qq.com"
	}
	client := &Client{
		appID: appID, secret: secret, base: base,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
	if secret != "" {
		var seed [ed25519.SeedSize]byte
		for i := range seed {
			seed[i] = secret[i%len(secret)]
		}
		client.webhookPrivateKey = ed25519.NewKeyFromSeed(seed[:])
		client.webhookPublicKey = client.webhookPrivateKey.Public().(ed25519.PublicKey)
	}
	return client
}

type APIError struct {
	Status int
	Code   int64
}

func (e *APIError) Error() string {
	return fmt.Sprintf("QQ API error (HTTP %d, code %d)", e.Status, e.Code)
}

func (c *Client) AccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Now().Before(c.refresh) {
		return c.token, nil
	}
	var response struct {
		Token     string          `json:"access_token"`
		ExpiresIn json.RawMessage `json:"expires_in"`
	}
	err := c.request(ctx, apiBase, http.MethodPost, "/app/getAppAccessToken", "", map[string]string{
		"appId": c.appID, "clientSecret": c.secret,
	}, &response)
	if err != nil {
		return "", err
	}
	var ttlText string
	if len(response.ExpiresIn) > 0 && response.ExpiresIn[0] == '"' {
		_ = json.Unmarshal(response.ExpiresIn, &ttlText)
	} else {
		ttlText = string(response.ExpiresIn)
	}
	ttl, err := strconv.Atoi(ttlText)
	if err != nil || ttl <= 0 || ttl > 86400 || response.Token == "" {
		return "", errors.New("QQ returned an invalid access token response")
	}
	c.token = response.Token
	c.refresh = time.Now().Add(time.Duration(ttl-min(60, ttl/2)) * time.Second)
	return c.token, nil
}

func (c *Client) Do(ctx context.Context, method, path string, body, dest any) error {
	token, err := c.AccessToken(ctx)
	if err != nil {
		return err
	}
	err = c.request(ctx, c.base, method, path, token, body, dest)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized {
		c.tokenMu.Lock()
		if token == c.token {
			c.refresh = time.Time{}
		}
		c.tokenMu.Unlock()
	}
	return err
}

func (c *Client) request(ctx context.Context, base, method, path, token string, body, dest any) error {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return errors.New("invalid QQ API request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "QQBot "+token)
		req.Header.Set("X-Union-Appid", c.appID)
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var remote *url.Error
		if errors.As(err, &remote) {
			err = remote.Err
		}
		return fmt.Errorf("QQ API transport failure on %s: %w", method, err)
	}
	defer res.Body.Close()
	const maxResponse = 2 << 20
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil || len(raw) > maxResponse {
		return errors.New("QQ API response unreadable or too large")
	}
	var meta struct {
		Code int64 `json:"code"`
	}
	_ = json.Unmarshal(raw, &meta)
	if res.StatusCode < 200 || res.StatusCode >= 300 || meta.Code != 0 {
		return &APIError{Status: res.StatusCode, Code: meta.Code}
	}
	if dest != nil {
		if err := json.Unmarshal(raw, dest); err != nil {
			return errors.New("QQ API returned an unexpected response")
		}
	}
	return nil
}

func ConversationPath(kind, peer string) string {
	if kind == "group" {
		return "/v2/groups/" + url.PathEscape(peer)
	}
	return "/v2/users/" + url.PathEscape(peer)
}

var ErrInvalidImage = errors.New("image must be HTTPS or valid base64 image data within 6 MiB")

func (c *Client) UploadImage(ctx context.Context, kind, peer, image string) (string, error) {
	body := map[string]any{"file_type": 1, "srv_send_msg": false}
	if encoded, ok := strings.CutPrefix(image, "base64://"); ok {
		if len(encoded) > base64.StdEncoding.EncodedLen(6<<20) {
			return "", ErrInvalidImage
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || !strings.HasPrefix(http.DetectContentType(decoded), "image/") {
			return "", ErrInvalidImage
		}
		body["file_data"] = encoded
	} else {
		u, err := url.Parse(image)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(image) > 2048 {
			return "", ErrInvalidImage
		}
		body["url"] = image
	}
	var uploaded struct {
		FileInfo string `json:"file_info"`
	}
	err := c.Do(ctx, http.MethodPost, ConversationPath(kind, peer)+"/files", body, &uploaded)
	if err != nil {
		return "", err
	}
	if uploaded.FileInfo == "" {
		return "", errors.New("QQ returned no media file_info")
	}
	return uploaded.FileInfo, nil
}
