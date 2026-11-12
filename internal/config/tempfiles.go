package config

import (
	"fmt"
	"net"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type TempFiles struct {
	Enabled   bool          `yaml:"enabled"`
	Listen    string        `yaml:"listen"`
	PublicURL string        `yaml:"public_url,omitempty"`
	Directory string        `yaml:"directory"`
	MaxBytes  int64         `yaml:"max_bytes"`
	TTL       time.Duration `yaml:"ttl"`
}

var sharePath = regexp.MustCompile(`^(/[A-Za-z0-9_-]+)+$`)

func (t *TempFiles) validate(gatewayPort string) error {
	if !t.Enabled {
		return nil
	}
	_, port, err := net.SplitHostPort(t.Listen)
	if err != nil {
		return fmt.Errorf("「临时分享 → 监听地址」（tempfiles.listen）须包含主机和端口，例如 127.0.0.1:6701。")
	}
	number, err := strconv.ParseUint(port, 10, 16)
	if err != nil || number == 0 {
		return fmt.Errorf("「临时分享 → 监听地址」的端口须为 1 到 65535 的整数。")
	}
	gatewayNumber, _ := strconv.ParseUint(gatewayPort, 10, 16)
	if number == gatewayNumber {
		return fmt.Errorf("「临时分享 → 监听地址」（tempfiles.listen）与「网关服务 → 监听地址」（listen）使用了同一端口。请给临时分享另选端口。")
	}
	address, err := url.Parse(t.PublicURL)
	if err != nil || len(t.PublicURL) > 512 || address.Hostname() == "" || (address.Scheme != "http" && address.Scheme != "https") ||
		address.User != nil || address.RawQuery != "" || address.ForceQuery || address.Fragment != "" || address.RawPath != "" {
		return fmt.Errorf("「临时分享 → 公网地址」（tempfiles.public_url）须填写完整的 HTTP(S) 地址，例如 https://share.example.com/share。")
	}
	if port := address.Port(); port != "" {
		if n, err := strconv.ParseUint(port, 10, 16); err != nil || n == 0 {
			return fmt.Errorf("「临时分享 → 公网地址」的端口须为 1 到 65535 的整数。")
		}
	}
	address.Path = strings.TrimRight(address.Path, "/")
	if address.Path == "" {
		address.Path = "/share"
	}
	if !sharePath.MatchString(address.Path) || path.Clean(address.Path) != address.Path {
		return fmt.Errorf("「临时分享 → 公网地址」的路径只能使用字母、数字、下划线和连字符组成的路径段。")
	}
	t.PublicURL = address.String()
	if strings.TrimSpace(t.Directory) == "" {
		return fmt.Errorf("「临时分享 → 文件目录」（tempfiles.directory）不能为空。")
	}
	if t.MaxBytes < 8192 || t.MaxBytes > 1<<40 {
		return fmt.Errorf("「临时分享 → 总容量」（tempfiles.max_bytes）须在 8192 字节到 1 TiB 之间。")
	}
	if t.TTL < time.Second || t.TTL > 24*time.Hour {
		return fmt.Errorf("「临时分享 → 有效期」（tempfiles.ttl）须在 1 秒到 24 小时之间，例如 10m。")
	}
	return nil
}
