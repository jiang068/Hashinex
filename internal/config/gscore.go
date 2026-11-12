package config

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
)

// ExpandGScoreOutputs turns older per-input URL templates into independent
// outputs. New routes can point multiple inputs at the same concrete output.
func (cfg *Config) ExpandGScoreOutputs() bool {
	used := make(map[string]bool, len(cfg.Outputs))
	for _, out := range cfg.Outputs {
		used[out.ID] = true
	}
	var expanded bool
	var outputs []Output
	for _, out := range cfg.Outputs {
		if out.Protocol != GScore || !strings.Contains(out.URL, "{input}") {
			outputs = append(outputs, out)
			continue
		}
		expanded = true
		var sources []string
		for _, route := range cfg.Routes {
			if slices.Contains(route.Targets(), out.ID) && !slices.Contains(sources, route.From) {
				sources = append(sources, route.From)
			}
		}
		if len(sources) <= 1 {
			source := out.ID
			if len(sources) == 1 {
				source = sources[0]
			}
			out.URL = strings.ReplaceAll(out.URL, "{input}", source)
			outputs = append(outputs, out)
			continue
		}
		for _, source := range sources {
			id := out.ID + "-" + source
			if len(id) > 64 || used[id] {
				base := id[:min(len(id), 48)]
				for n := 0; ; n++ {
					digest := sha256.Sum256(fmt.Appendf(nil, "%s-%d", id, n))
					alternative := fmt.Sprintf("%s-%x", base, digest[:6])
					if !used[alternative] {
						id = alternative
						break
					}
				}
			}
			used[id] = true
			target := out
			target.ID = id
			target.URL = strings.ReplaceAll(out.URL, "{input}", source)
			outputs = append(outputs, target)
			for i := range cfg.Routes {
				route := &cfg.Routes[i]
				if route.From != source {
					continue
				}
				if route.To == out.ID {
					route.To = id
				}
				for j, name := range route.Outputs {
					if name == out.ID {
						route.Outputs[j] = id
					}
				}
			}
		}
	}
	if expanded {
		cfg.Outputs = outputs
	}
	return expanded
}
