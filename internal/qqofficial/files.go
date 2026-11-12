package qqofficial

import (
	"cmp"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const (
	MaxFileBytes  = 200_000_000
	directFileMax = 5_000_000
	prefixMD5Size = 10_002_432
)

func (c *Client) UploadFileURL(ctx context.Context, kind, peer, name, address string) (string, error) {
	return c.uploadMedia(ctx, ConversationPath(kind, peer), map[string]any{
		"file_type": 4, "file_name": name, "url": address, "srv_send_msg": false,
	})
}

func (c *Client) UploadFile(ctx context.Context, kind, peer, name string, file io.ReaderAt, size int64) (string, error) {
	if size <= 0 || size > MaxFileBytes {
		return "", errors.New("file size is outside the QQ upload limit")
	}
	path := ConversationPath(kind, peer)
	body := map[string]any{"file_type": 4, "file_name": name, "srv_send_msg": false}
	if size <= directFileMax {
		raw := make([]byte, size)
		if _, err := io.ReadFull(io.NewSectionReader(file, 0, size), raw); err != nil {
			return "", errors.New("could not read complete local file")
		}
		body["file_data"] = base64.StdEncoding.EncodeToString(raw)
	} else {
		id, err := c.uploadFileParts(ctx, path, peer, name, file, size)
		if err != nil {
			return "", err
		}
		body["upload_id"] = id
	}
	return c.uploadMedia(ctx, path, body)
}

type uploadPart struct {
	Index int    `json:"index"`
	URL   string `json:"presigned_url"`
}

type FileUploadPrepareError struct {
	*APIError
	FileSize  int64
	QQMessage string
}

func (e *FileUploadPrepareError) Error() string {
	return "QQ file upload prepare: " + e.APIError.Error()
}

func (e *FileUploadPrepareError) Unwrap() error { return e.APIError }

func (c *Client) uploadFileParts(ctx context.Context, path, peer, name string, file io.ReaderAt, size int64) (string, error) {
	md5Full, md5Prefix, sha1Full := md5.New(), md5.New(), sha1.New()
	prefixSize := min(size, int64(prefixMD5Size))
	reader := io.NewSectionReader(file, 0, size)
	if _, err := io.CopyN(io.MultiWriter(md5Full, sha1Full, md5Prefix), reader, prefixSize); err != nil {
		return "", errors.New("could not read local file for QQ upload")
	}
	if _, err := io.CopyN(io.MultiWriter(md5Full, sha1Full), reader, size-prefixSize); err != nil {
		return "", errors.New("could not read local file for QQ upload")
	}
	md5Hex := hex.EncodeToString(md5Full.Sum(nil))
	sha1Hex := hex.EncodeToString(sha1Full.Sum(nil))
	prefixHex := hex.EncodeToString(md5Prefix.Sum(nil))
	request := map[string]any{
		"file_type": 4, "file_name": name, "file_size": strconv.FormatInt(size, 10),
		"md5": md5Hex, "sha1": sha1Hex, "md5_10m": prefixHex,
	}
	var prepared struct {
		ID        string       `json:"upload_id"`
		BlockSize json.Number  `json:"block_size"`
		Parts     []uploadPart `json:"parts"`
	}
	if err := c.Do(ctx, http.MethodPost, path+"/upload_prepare", request, &prepared); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			message := redactQQMessage(apiErr.message, name, peer, md5Hex, sha1Hex, prefixHex)
			return "", &FileUploadPrepareError{APIError: apiErr, FileSize: size, QQMessage: fileUploadDiagnostic(message)}
		}
		return "", fmt.Errorf("QQ file upload prepare: %w", err)
	}
	block, err := prepared.BlockSize.Int64()
	if err != nil || block <= 0 || prepared.ID == "" || int64(len(prepared.Parts)) != (size-1)/block+1 {
		return "", errors.New("QQ returned an invalid file upload plan")
	}
	slices.SortFunc(prepared.Parts, func(a, b uploadPart) int { return cmp.Compare(a.Index, b.Index) })
	for index, part := range prepared.Parts {
		u, err := url.Parse(part.URL)
		if part.Index != index || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return "", errors.New("QQ returned an invalid file upload part")
		}
	}
	for _, part := range prepared.Parts {
		offset := int64(part.Index) * block
		length := min(block, size-offset)
		digest := md5.New()
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, part.URL, io.TeeReader(io.NewSectionReader(file, offset, length), digest))
		if err != nil {
			return "", errors.New("could not prepare QQ file upload part")
		}
		req.ContentLength = length
		req.Header.Set("Content-Type", "application/octet-stream")
		res, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", errors.New("QQ file upload part transport failure")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		_ = res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return "", fmt.Errorf("QQ file upload part returned HTTP %d", res.StatusCode)
		}
		confirm := map[string]any{
			"upload_id": prepared.ID, "part_index": part.Index, "block_size": strconv.FormatInt(length, 10),
			"md5": hex.EncodeToString(digest.Sum(nil)),
		}
		if err := c.Do(ctx, http.MethodPost, path+"/upload_part_finish", confirm, nil); err != nil {
			return "", fmt.Errorf("QQ file upload part confirmation: %w", err)
		}
	}
	return prepared.ID, nil
}

func fileUploadDiagnostic(message string) string {
	message = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, message)
	words := strings.Fields(message)
	for i, word := range words {
		if strings.ContainsAny(word, `/\`) {
			words[i] = "[redacted]"
		}
	}
	const maxRunes = 512
	runes := []rune(strings.Join(words, " "))
	return string(runes[:min(len(runes), maxRunes)])
}
