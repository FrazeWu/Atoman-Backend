package handlers

import "strings"

func handlerContainsPattern(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
	return "%" + value + "%"
}
