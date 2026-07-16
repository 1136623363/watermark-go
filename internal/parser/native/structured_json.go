package native

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"

	coreparser "github.com/1136623363/watermark-go/internal/parser"
)

var errStructuredJSONNotFound = errors.New("structured JSON carrier not found")

func extractStructuredJSON(document []byte, markers ...string) (json.RawMessage, string, error) {
	lower := strings.ToLower(string(document))
	switch {
	case strings.Contains(lower, "login required"), strings.Contains(lower, "sign in to continue"):
		return nil, "", coreparser.NewParseError(coreparser.ErrorCredentialRequired, errors.New("upstream requires a credential"))
	case strings.Contains(lower, "captcha"), strings.Contains(lower, "risk control"), strings.Contains(lower, "access denied"):
		return nil, "", coreparser.NewParseError(coreparser.ErrorSecurityRejected, errors.New("upstream risk control rejected the request"))
	}

	var lastErr error
	for _, marker := range markers {
		searchFrom := 0
		for searchFrom < len(document) {
			relative := bytes.Index(document[searchFrom:], []byte(marker))
			if relative < 0 {
				break
			}
			start := searchFrom + relative + len(marker)
			payload, err := structuredValueAt(document, start)
			if err == nil {
				return payload, marker, nil
			}
			lastErr = err
			searchFrom = start
		}
	}
	if lastErr == nil {
		lastErr = errStructuredJSONNotFound
	}
	return nil, "", coreparser.NewParseError(coreparser.ErrorSchemaChanged, lastErr)
}

func structuredValueAt(document []byte, start int) (json.RawMessage, error) {
	index := skipStructuredSeparators(document, start)
	if index >= len(document) {
		return nil, errors.New("structured JSON is truncated")
	}
	if bytes.HasPrefix(document[index:], []byte("JSON.parse")) {
		index += len("JSON.parse")
		index = skipSpace(document, index)
		if index >= len(document) || document[index] != '(' {
			return nil, errors.New("invalid JSON.parse carrier")
		}
		index = skipSpace(document, index+1)
		var encoded string
		decoder := json.NewDecoder(bytes.NewReader(document[index:]))
		if err := decoder.Decode(&encoded); err != nil {
			return nil, errors.New("truncated escaped structured JSON")
		}
		payload := json.RawMessage(encoded)
		if !json.Valid(payload) {
			return nil, errors.New("invalid escaped structured JSON")
		}
		return append(json.RawMessage(nil), payload...), nil
	}
	if document[index] == '"' {
		var encoded string
		decoder := json.NewDecoder(bytes.NewReader(document[index:]))
		if err := decoder.Decode(&encoded); err != nil {
			return nil, errors.New("truncated quoted structured JSON")
		}
		payload := json.RawMessage(encoded)
		if !json.Valid(payload) {
			return nil, errors.New("invalid quoted structured JSON")
		}
		return append(json.RawMessage(nil), payload...), nil
	}
	if document[index] != '{' && document[index] != '[' {
		return nil, errors.New("structured JSON does not begin with an object or array")
	}
	end, err := scanStructuredValue(document, index)
	if err != nil {
		return nil, err
	}
	payload := append(json.RawMessage(nil), document[index:end]...)
	if !json.Valid(payload) {
		return nil, errors.New("invalid structured JSON")
	}
	return payload, nil
}

func skipStructuredSeparators(document []byte, index int) int {
	index = skipSpace(document, index)
	if index < len(document) && (document[index] == '=' || document[index] == ':') {
		index = skipSpace(document, index+1)
	}
	return index
}

func skipSpace(document []byte, index int) int {
	for index < len(document) {
		switch document[index] {
		case ' ', '\t', '\r', '\n':
			index++
		default:
			return index
		}
	}
	return index
}

func scanStructuredValue(document []byte, start int) (int, error) {
	stack := []byte{document[start]}
	inString := false
	escaped := false
	for index := start + 1; index < len(document); index++ {
		current := document[index]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if current == '\\' {
				escaped = true
				continue
			}
			if current == '"' {
				inString = false
			}
			continue
		}
		switch current {
		case '"':
			inString = true
		case '{', '[':
			stack = append(stack, current)
		case '}', ']':
			opening := stack[len(stack)-1]
			if (opening == '{' && current != '}') || (opening == '[' && current != ']') {
				return 0, errors.New("mismatched structured JSON delimiters")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return index + 1, nil
			}
		}
	}
	return 0, errors.New("structured JSON is truncated")
}

func findKuaishouSnapshot(payload json.RawMessage) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("invalid Kuaishou snapshot"))
	}
	matched := findObjectWithFields(value, 0, "result", "photo")
	if matched == nil {
		return nil, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("Kuaishou snapshot fields moved"))
	}
	photo, _ := matched["photo"].(map[string]any)
	if photo == nil || !hasKuaishouCoreMedia(photo) {
		return nil, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("Kuaishou snapshot has no core media"))
	}
	encoded, err := json.Marshal(matched)
	if err != nil {
		return nil, coreparser.NewParseError(coreparser.ErrorSchemaChanged, errors.New("Kuaishou snapshot cannot be normalized"))
	}
	return encoded, nil
}

func findObjectWithFields(value any, depth int, fields ...string) map[string]any {
	if depth > 16 {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		matched := true
		for _, field := range fields {
			if _, ok := typed[field]; !ok {
				matched = false
				break
			}
		}
		if matched {
			return typed
		}
		for _, child := range typed {
			if found := findObjectWithFields(child, depth+1, fields...); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range typed {
			if found := findObjectWithFields(child, depth+1, fields...); found != nil {
				return found
			}
		}
	}
	return nil
}

func hasKuaishouCoreMedia(photo map[string]any) bool {
	for _, key := range []string{"mainMvUrls", "ext_params"} {
		if value, exists := photo[key]; exists && value != nil {
			return true
		}
	}
	return false
}
