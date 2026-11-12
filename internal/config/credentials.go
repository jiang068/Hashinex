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

func (cfg *Config) credentialRefs() []string {
	refs := []string{cfg.Admin.TokenRef}
	for _, in := range cfg.Inputs {
		refs = append(refs, in.TokenRef, in.SecretRef)
	}
	for _, out := range cfg.Outputs {
		refs = append(refs, out.TokenRef)
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

func readCredentials(cfg *Config, path string, overrides map[string]string) (map[string]string, error) {
	values := make(map[string]string)
	var loaded bool
	for _, ref := range cfg.credentialRefs() {
		if ref == "" {
			continue
		}
		if value, ok := overrides[ref]; ok && value != "" {
			values[ref] = value
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
		if values[ref] == "" {
			return nil, fmt.Errorf("a saved credential is missing")
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
		return nil, fmt.Errorf("could not locate the credential file")
	}
	file, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("could not read saved credentials")
	}
	defer file.Close()
	var values map[string]string
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	if err := decoder.Decode(&values); err != nil {
		return nil, fmt.Errorf("could not decode saved credentials")
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
	values, err := readCredentials(prior, path, nil)
	if err != nil {
		return "", nil, err
	}
	keep := make(map[string]string)
	for _, ref := range append(prior.credentialRefs(), next.credentialRefs()...) {
		if value := values[ref]; value != "" {
			keep[ref] = value
		}
		if value := secrets[ref]; value != "" {
			keep[ref] = value
		}
	}
	data, err := json.Marshal(keep)
	if err != nil || len(data) >= 1<<20 {
		return "", nil, fmt.Errorf("credential storage exceeds its size limit")
	}
	name, err := credentialPath(path)
	if err != nil {
		return "", nil, fmt.Errorf("could not locate the credential file")
	}
	return name, data, nil
}
