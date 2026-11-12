package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"gopkg.in/yaml.v3"
	"hashinex/internal/config"
)

var settingKeys = []string{"listen", "public_ws_url", "log_level", "inputs", "outputs", "routes", "storage", "matching", "limits", "logging", "admin"}

func readSettings(raw []byte, path string) (map[string]any, error) {
	cfg, err := config.ParseAt(raw, path)
	if err != nil {
		return nil, err
	}
	cfg.ExpandGScoreOutputs()
	if err := cfg.UpgradeQQScopes(path); err != nil {
		return nil, err
	}
	all, err := settingsValues(cfg)
	if err != nil {
		return nil, err
	}
	result := make(map[string]any, len(settingKeys))
	for _, key := range settingKeys {
		result[key] = all[key]
	}
	// Preserve the full uint64 value when the browser edits an input.
	if inputs, ok := result["inputs"].([]any); ok {
		for i, input := range cfg.Inputs {
			if input.Intents != 0 {
				inputs[i].(map[string]any)["intents"] = strconv.FormatUint(input.Intents, 10)
			}
		}
	}
	return result, nil
}

func settingsValues(cfg *config.Config) (map[string]any, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	err = yaml.Unmarshal(data, &values)
	return values, err
}

func mergeSettings(current, update []byte) ([]byte, error) {
	var changes map[string]json.RawMessage
	if err := json.Unmarshal(update, &changes); err != nil || len(changes) == 0 {
		return nil, fmt.Errorf("expected a JSON object with changed settings")
	}
	for key := range changes {
		if !slices.Contains(settingKeys, key) {
			return nil, fmt.Errorf("unsupported setting %q", key)
		}
	}
	if _, editsOutputs := changes["outputs"]; editsOutputs || changes["routes"] != nil {
		cfg, err := config.Parse(current)
		if err != nil {
			return nil, err
		}
		if cfg.ExpandGScoreOutputs() {
			values, err := settingsValues(cfg)
			if err != nil {
				return nil, err
			}
			for _, key := range []string{"outputs", "routes"} {
				if _, changed := changes[key]; !changed {
					changes[key], err = json.Marshal(values[key])
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	var document yaml.Node
	if err := yaml.Unmarshal(current, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("configuration is not a YAML mapping")
	}
	root := document.Content[0]
	for _, key := range settingKeys {
		raw, changed := changes[key]
		if !changed {
			continue
		}
		var node yaml.Node
		if err := yaml.Unmarshal(raw, &node); err != nil || len(node.Content) != 1 {
			return nil, fmt.Errorf("invalid setting %q", key)
		}
		value := node.Content[0]
		normalizeSettingNode(value)
		if key == "inputs" && value.Kind == yaml.SequenceNode {
			for _, input := range value.Content {
				if input.Kind != yaml.MappingNode {
					continue
				}
				for i := 0; i < len(input.Content); i += 2 {
					if input.Content[i].Value == "intents" {
						intent := input.Content[i+1]
						if _, err := strconv.ParseUint(intent.Value, 10, 64); err != nil {
							return nil, fmt.Errorf("intents must be an unsigned integer")
						}
						intent.Tag, intent.Style = "!!int", 0
					}
				}
			}
		}
		index := -1
		for i := 0; i < len(root.Content); i += 2 {
			if root.Content[i].Value == key {
				index = i + 1
				break
			}
		}
		if index < 0 {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
			continue
		}
		value.HeadComment = root.Content[index].HeadComment
		value.LineComment = root.Content[index].LineComment
		value.FootComment = root.Content[index].FootComment
		root.Content[index] = value
	}
	return yaml.Marshal(&document)
}

func normalizeSettingNode(node *yaml.Node) {
	node.Style = 0
	for _, child := range node.Content {
		normalizeSettingNode(child)
	}
}

func extractCredentialEdits(update []byte) ([]byte, map[string]string, error) {
	var changes map[string]json.RawMessage
	if err := json.Unmarshal(update, &changes); err != nil {
		return nil, nil, fmt.Errorf("invalid settings draft")
	}
	secrets := make(map[string]string)
	updateEntry := func(entry map[string]json.RawMessage, kind string) error {
		key := kind + "_value"
		raw, exists := entry[key]
		if !exists {
			return nil
		}
		delete(entry, key)
		var value *string
		if err := json.Unmarshal(raw, &value); err != nil || (value != nil && (len(*value) == 0 || len(*value) > 4096)) {
			return fmt.Errorf("invalid credential value")
		}
		delete(entry, kind+"_ref")
		delete(entry, kind+"_env")
		if value != nil {
			var random [16]byte
			rand.Read(random[:])
			ref := hex.EncodeToString(random[:])
			secrets[ref] = *value
			entry[kind+"_ref"] = json.RawMessage(strconv.Quote(ref))
		}
		return nil
	}
	for _, key := range []string{"inputs", "outputs"} {
		if raw, ok := changes[key]; ok {
			var entries []map[string]json.RawMessage
			if err := json.Unmarshal(raw, &entries); err != nil {
				return nil, nil, fmt.Errorf("invalid %s draft", key)
			}
			for _, entry := range entries {
				for _, kind := range []string{"token", "secret"} {
					if err := updateEntry(entry, kind); err != nil {
						return nil, nil, err
					}
				}
			}
			changes[key], _ = json.Marshal(entries)
		}
	}
	if raw, ok := changes["admin"]; ok {
		var entry map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entry); err != nil {
			return nil, nil, fmt.Errorf("invalid admin settings")
		}
		if err := updateEntry(entry, "token"); err != nil {
			return nil, nil, err
		}
		changes["admin"], _ = json.Marshal(entry)
	}
	data, err := json.Marshal(changes)
	return data, secrets, err
}
