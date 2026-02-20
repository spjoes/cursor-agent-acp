package tools

import (
	"fmt"
	"strconv"
	"strings"
)

func getString(params map[string]any, key string) string {
	if params == nil {
		return ""
	}
	if v, ok := params[key]; ok {
		switch x := v.(type) {
		case string:
			return x
		default:
			return fmt.Sprint(v)
		}
	}
	return ""
}

func getBool(params map[string]any, key string, def bool) bool {
	if params == nil {
		return def
	}
	if v, ok := params[key]; ok {
		switch x := v.(type) {
		case bool:
			return x
		case string:
			return strings.EqualFold(x, "true")
		case float64:
			return x != 0
		case int:
			return x != 0
		}
	}
	return def
}

func getInt(params map[string]any, key string, def int) int {
	if params == nil {
		return def
	}
	if v, ok := params[key]; ok {
		switch x := v.(type) {
		case int:
			return x
		case int64:
			return int(x)
		case float64:
			return int(x)
		case float32:
			return int(x)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
				return n
			}
		}
	}
	return def
}
