package config

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"hashinex/internal/store"
)

// UpgradeQQScopes uses existing mappings once, when reading a legacy config.
// Unmapped entries must be replaced with real IDs before the scope can load.
func (cfg *Config) UpgradeQQScopes(path string) error {
	type scope struct {
		input, field string
		keys         []store.Identity
		dest         *[]string
		replace      bool
	}
	var scopes []scope
	var allKeys []store.Identity
	for i := range cfg.Inputs {
		in := &cfg.Inputs[i]
		if in.Protocol != QQOfficial || (len(in.GroupOpenIDs) == 0 && len(in.PrivateOpenIDs) == 0) {
			continue
		}
		groupNS := "qq:" + in.AppID
		for _, list := range []struct {
			namespace, kind, field string
			values                 []string
			dest                   *[]string
			mixed                  bool
		}{
			{groupNS, "group", "group_ids", in.GroupOpenIDs, &in.GroupIDs, false},
			{groupNS + ":c2c", "user", "private_qq_ids", in.PrivateOpenIDs, &in.PrivateQQIDs, false},
			{groupNS, "group", "silent_groups", in.SilentGroups, &in.SilentGroups, true},
			{groupNS, "group", "business_groups", in.BusinessGroups, &in.BusinessGroups, true},
		} {
			if !list.mixed && len(*list.dest) > 0 {
				continue
			}
			var keys []store.Identity
			for _, native := range list.values {
				if !list.mixed || !validQQID(native) {
					keys = append(keys, store.Identity{Namespace: list.namespace, Kind: list.kind, Native: native})
				}
			}
			if len(keys) > 0 {
				scopes = append(scopes, scope{input: in.ID, field: list.field, keys: keys, dest: list.dest, replace: list.mixed})
				allKeys = append(allKeys, keys...)
			}
		}
		in.GroupOpenIDs, in.PrivateOpenIDs = nil, nil
	}
	if len(allKeys) == 0 {
		return nil
	}
	dbPath := cfg.Storage.SQLite
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(filepath.Dir(path), dbPath)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ids, err := store.ReadRealIDs(ctx, dbPath, allKeys)
	if err != nil {
		return fmt.Errorf("旧版 QQ 群或私聊白名单需要从身份数据库转换 OpenID，但读取失败。请检查「身份数据库」的 SQLite 文件和读取权限。")
	}
	for _, scope := range scopes {
		values := *scope.dest
		if scope.replace {
			values = slices.DeleteFunc(values, func(id string) bool { return !validQQID(id) })
		}
		for _, key := range scope.keys {
			if ids[key] == 0 {
				return fmt.Errorf("输入 %q 的 %s 含尚未绑定的旧 OpenID。请在身份映射中补齐关联，或在 YAML 中用真实群号或 QQ 号填写 %s，并移除对应的旧 OpenID 配置。", scope.input, scope.field, scope.field)
			}
			id := strconv.FormatInt(ids[key], 10)
			if !slices.Contains(values, id) {
				values = append(values, id)
			}
		}
		*scope.dest = values
	}
	return nil
}

func validQQID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && n < store.VirtualBase && strconv.FormatInt(n, 10) == id
}
