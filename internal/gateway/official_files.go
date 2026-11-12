package gateway

import (
	"cmp"
	"context"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"hashinex/internal/qqofficial"
)

type officialFileLocation struct {
	localPath string
	address   string
	name      string
}

func (c call) uploadOfficialFile(ctx context.Context, kind, peer string, data map[string]string) (string, error) {
	location, err := officialFilePath(cmp.Or(data["file"], data["url"]), cmp.Or(data["name"], data["file_name"]))
	if err != nil {
		return "", err
	}
	if location.address != "" {
		return c.input.official.UploadFileURL(ctx, kind, peer, location.name, location.address)
	}
	source, err := c.openOfficialFile(location.localPath)
	if err != nil {
		return "", err
	}
	defer source.close()
	if source.info.Size() <= 0 || source.info.Size() > qqofficial.MaxFileBytes {
		return "", actionError{1400, "文件大小须大于 0，且不超过 QQ 的 200 MB 上传上限。"}
	}
	return c.input.official.UploadFile(ctx, kind, peer, location.name, source.file, source.info.Size())
}

func officialFilePath(source, name string) (officialFileLocation, error) {
	localPath := source
	var address string
	if !filepath.IsAbs(localPath) {
		u, err := url.Parse(source)
		if err != nil || u.User != nil || u.Opaque != "" || u.Fragment != "" {
			return officialFileLocation{}, actionError{1400, "文件地址格式无效。请提供本地绝对路径、file:// 或可公开访问的 HTTP(S) URL。"}
		}
		switch u.Scheme {
		case "http", "https":
			if u.Hostname() == "" {
				return officialFileLocation{}, actionError{1400, "文件 URL 缺少主机。"}
			}
			address = source
			if name == "" {
				name = path.Base(u.Path)
			}
		case "file":
			if (u.Host != "" && !strings.EqualFold(u.Host, "localhost")) || u.RawQuery != "" {
				return officialFileLocation{}, actionError{1400, "file:// 须指向共享目录中的本地文件。"}
			}
			localPath = filepath.FromSlash(u.Path)
			if len(localPath) > 1 && os.IsPathSeparator(localPath[0]) && filepath.VolumeName(localPath[1:]) != "" {
				localPath = localPath[1:]
			}
			if !filepath.IsAbs(localPath) {
				return officialFileLocation{}, actionError{1400, "file:// 须提供绝对路径。"}
			}
		default:
			return officialFileLocation{}, actionError{1400, "请提供本地绝对路径、file:// 或可公开访问的 HTTP(S) 文件 URL。"}
		}
	}
	if name == "" {
		name = filepath.Base(localPath)
	}
	if !validOfficialFilename(name) {
		return officialFileLocation{}, actionError{1400, "请提供有效的文件名。"}
	}
	return officialFileLocation{localPath: localPath, address: address, name: name}, nil
}

type localOfficialFile struct {
	root *os.Root
	file *os.File
	info os.FileInfo
}

func (f *localOfficialFile) close() {
	_ = f.file.Close()
	_ = f.root.Close()
}

func (c call) openOfficialFile(localPath string) (*localOfficialFile, error) {
	dir := c.route.output.OfficialFileRoot
	if dir == "" {
		return nil, actionError{1403, "该输出未配置「官 Bot 本地文件目录」。请在 WebUI 的「输出管理」设置共享目录。"}
	}
	relative, err := filepath.Rel(dir, localPath)
	if err != nil || !filepath.IsLocal(relative) || relative == "." {
		return nil, actionError{1403, "文件不在该输出的「官 Bot 本地文件目录」内。请检查共享目录和文件路径。"}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, actionError{1404, "网关无法打开共享目录。请检查挂载和读取权限。"}
	}
	info, err := root.Stat(relative)
	if err != nil || !info.Mode().IsRegular() {
		_ = root.Close()
		return nil, actionError{1404, "网关无法读取共享目录内的普通文件。请检查挂载、文件位置和读取权限。"}
	}
	file, err := root.Open(relative)
	if err != nil {
		_ = root.Close()
		return nil, actionError{1404, "网关无法打开共享文件。请检查文件和读取权限。"}
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		_ = root.Close()
		return nil, actionError{1404, "网关无法读取共享文件。"}
	}
	return &localOfficialFile{root: root, file: file, info: info}, nil
}

func validOfficialFilename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\") && strings.IndexFunc(name, unicode.IsControl) == -1
}
