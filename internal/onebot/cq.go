package onebot

import (
	"errors"
	"strings"
)

type Segment struct {
	Type string            `json:"type"`
	Data map[string]string `json:"data"`
}

var (
	cqTextDecoder  = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&amp;", "&")
	cqParamDecoder = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&")
)

func ParseCQ(text string) ([]Segment, error) {
	var out []Segment
	for text != "" {
		prefix, rest, has := strings.Cut(text, "[CQ:")
		if prefix != "" {
			out = append(out, Segment{"text", map[string]string{"text": cqTextDecoder.Replace(prefix)}})
		}
		if !has {
			break
		}
		cq, after, closed := strings.Cut(rest, "]")
		if !closed || len(out) > 126 {
			return nil, errors.New("invalid or oversized CQ message")
		}
		parts := strings.Split(cq, ",")
		seg := Segment{Type: parts[0], Data: make(map[string]string)}
		for _, parameter := range parts[1:] {
			key, value, ok := strings.Cut(parameter, "=")
			if !ok || key == "" {
				return nil, errors.New("invalid CQ segment")
			}
			seg.Data[key] = cqParamDecoder.Replace(value)
		}
		out = append(out, seg)
		text = after
	}
	return out, nil
}
