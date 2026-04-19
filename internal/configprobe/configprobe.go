package configprobe

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

func ReadJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func ReadTopLevelJSONString(path, key string) (string, bool, error) {
	return ReadNestedJSONString(path, key)
}

func ReadNestedJSONString(path string, keys ...string) (string, bool, error) {
	payload, err := ReadJSONObject(path)
	if err != nil {
		return "", false, err
	}
	var current any = payload
	for _, key := range keys {
		object, ok := current.(map[string]any)
		if !ok {
			return "", false, nil
		}
		value, ok := object[key]
		if !ok {
			return "", false, nil
		}
		current = value
	}
	text, ok := current.(string)
	if !ok {
		return "", false, nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false, nil
	}
	return text, true, nil
}

func ReadSimpleTOMLString(path, key string) (string, bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	prefix := key + " = "
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "[") {
			continue
		}
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		raw := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		if value, ok := trimQuoted(raw); ok {
			return value, true, nil
		}
		return "", false, nil
	}
	if err := scanner.Err(); err != nil {
		return "", false, err
	}
	return "", false, nil
}

func trimQuoted(value string) (string, bool) {
	if len(value) < 2 {
		return "", false
	}
	if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
		return strings.TrimSpace(value[1 : len(value)-1]), true
	}
	if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
		return strings.TrimSpace(value[1 : len(value)-1]), true
	}
	return "", false
}
