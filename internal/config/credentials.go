package config

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func credential(values map[string]string, ref, env string) string {
	if ref != "" {
		return values[ref]
	}
	return os.Getenv(env)
}

type credentialRef struct {
	key, location string
}

func (cfg *Config) credentialRefs() []credentialRef {
	refs := []credentialRef{{cfg.Admin.TokenRef, "「管理访问 → 管理令牌」（admin.token_ref）"}}
	for i, in := range cfg.Inputs {
		location := fmt.Sprintf("第 %d 个输入", i+1)
		if namePattern.MatchString(in.ID) {
			location = fmt.Sprintf("输入 %q", in.ID)
		}
		refs = append(refs, credentialRef{in.TokenRef, location + "的连接令牌（token_ref）"},
			credentialRef{in.SecretRef, location + "的 AppSecret（secret_ref）"})
	}
	for i, out := range cfg.Outputs {
		location := fmt.Sprintf("第 %d 个输出", i+1)
		if namePattern.MatchString(out.ID) {
			location = fmt.Sprintf("输出 %q", out.ID)
		}
		refs = append(refs, credentialRef{out.TokenRef, location + "的连接令牌（token_ref）"})
	}
	return refs
}

func credentialPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute, err = filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(absolute)
	name := filepath.Base(absolute) + ".secrets.json"
	for parent := dir; ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil {
			dir = filepath.Join(filepath.Dir(parent), "cache", filepath.Base(parent))
			sum := sha256.Sum256([]byte(absolute))
			name = fmt.Sprintf("%x.secrets.json", sum[:12])
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	return filepath.Join(dir, name), nil
}

func readCredentials(cfg *Config, path string, overrides map[string]string, requireRefs bool) (map[string]string, error) {
	values := make(map[string]string)
	var loaded bool
	for _, ref := range cfg.credentialRefs() {
		if ref.key == "" {
			continue
		}
		if value, ok := overrides[ref.key]; ok && value != "" {
			values[ref.key] = value
			continue
		}
		if !loaded {
			saved, err := loadCredentialFile(path)
			if err != nil {
				return nil, err
			}
			for key, value := range saved {
				values[key] = value
			}
			loaded = true
		}
		if requireRefs && values[ref.key] == "" {
			return nil, fmt.Errorf("%s引用的凭证缺失。请在相应表单重新录入，或从备份恢复此配置对应的密钥文件。", ref.location)
		}
	}
	for key, value := range overrides {
		values[key] = value
	}
	return values, nil
}

func loadCredentialFile(path string) (map[string]string, error) {
	name, err := credentialPath(path)
	if err != nil {
		return nil, fmt.Errorf("无法定位配置对应的密钥文件。请检查配置文件及所在目录是否可访问。")
	}
	file, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("无法读取配置对应的密钥文件。请检查网关进程的读取权限。")
	}
	defer file.Close()
	var values map[string]string
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("密钥文件格式有误。请从备份恢复配置对应的密钥文件。")
	}
	return values, nil
}

// PrepareUpdate validates the draft against its new credentials. The previous
// references remain available until the YAML has been replaced.
func PrepareUpdate(current, draft []byte, path string, secrets map[string]string) (string, []byte, error) {
	next, err := ParseAt(draft, path)
	if err != nil {
		return "", nil, err
	}
	if _, err := resolve(next, path, secrets); err != nil {
		return "", nil, err
	}
	if len(secrets) == 0 {
		return "", nil, nil
	}
	prior, err := ParseAt(current, path)
	if err != nil {
		return "", nil, err
	}
	values, err := readCredentials(prior, path, nil, false)
	if err != nil {
		return "", nil, err
	}
	keep := make(map[string]string)
	for _, ref := range append(prior.credentialRefs(), next.credentialRefs()...) {
		if value := values[ref.key]; value != "" {
			keep[ref.key] = value
		}
		if value := secrets[ref.key]; value != "" {
			keep[ref.key] = value
		}
	}
	data, err := json.Marshal(keep)
	if err != nil || len(data) >= 1<<20 {
		return "", nil, fmt.Errorf("密钥文件将超过 1 MiB。请清理未使用的密钥引用后再保存。")
	}
	name, err := credentialPath(path)
	if err != nil {
		return "", nil, fmt.Errorf("无法定位密钥文件的保存目录。请检查配置文件及所在目录是否可访问。")
	}
	return name, data, nil
}
