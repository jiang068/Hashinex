package config

import (
	"fmt"
	"io"
	"regexp"
	"strings"
)

var yamlErrorLine = regexp.MustCompile("\\bline ([0-9]+): ([^\\r\\n]+)")

func yamlConfigError(err error) error {
	if err == io.EOF {
		return fmt.Errorf("配置文件为空。请填写 YAML 配置后再保存。")
	}
	// YAML decoder errors can include literal values from the draft.
	if match := yamlErrorLine.FindStringSubmatch(err.Error()); match != nil {
		line, detail := match[1], match[2]
		switch {
		case strings.HasPrefix(detail, "field ") && strings.Contains(detail, " not found in type "):
			return fmt.Errorf("YAML 第 %s 行的字段名不受支持。请检查拼写及所属配置段的缩进。", line)
		case strings.HasPrefix(detail, "mapping key ") && strings.Contains(detail, " already defined"):
			return fmt.Errorf("YAML 第 %s 行与同一配置段中的字段重名。请合并重复字段。", line)
		case strings.HasPrefix(detail, "cannot unmarshal "):
			return fmt.Errorf("YAML 第 %s 行的值类型不符合字段要求。请核对该字段应填写的数字、布尔值、文本或列表格式。", line)
		default:
			return fmt.Errorf("YAML 第 %s 行附近的语法有误。请检查缩进、冒号、引号和列表符号。", line)
		}
	}
	return fmt.Errorf("YAML 格式无效。请检查配置是否为由字段和值组成的映射，以及缩进和列表格式。")
}
