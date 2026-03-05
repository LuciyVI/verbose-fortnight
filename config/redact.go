package config

import (
	"encoding/json"
	"reflect"
	"strings"
)

const redactedValue = "***redacted***"

var redactedFieldTokens = []string{
	"apikey",
	"apisecret",
	"token",
	"password",
	"secret",
	"key",
}

func shouldRedactField(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return false
	}
	for _, token := range redactedFieldTokens {
		if strings.Contains(n, token) {
			return true
		}
	}
	return false
}

// RedactedMap returns effective config as a map with sensitive values masked.
func RedactedMap(cfg *Config) map[string]any {
	out := make(map[string]any)
	if cfg == nil {
		return out
	}
	rv := reflect.ValueOf(*cfg)
	rt := reflect.TypeOf(*cfg)
	for i := 0; i < rv.NumField(); i++ {
		field := rt.Field(i)
		name := field.Name
		if shouldRedactField(name) {
			out[name] = redactedValue
			continue
		}
		out[name] = rv.Field(i).Interface()
	}
	return out
}

// RedactedJSON returns effective config JSON with sensitive values masked.
func RedactedJSON(cfg *Config) ([]byte, error) {
	return json.Marshal(RedactedMap(cfg))
}
