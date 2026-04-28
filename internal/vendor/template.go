package vendor

import (
	"fmt"
	"regexp"
)

var placeholderRe = regexp.MustCompile(`\{\{(\w+)\}\}`)

// RenderTemplate replaces {{key}} placeholders in tmpl with values from payload.
// Returns an error if a placeholder key is not found in payload.
func RenderTemplate(tmpl string, payload map[string]interface{}) (string, error) {
	var renderErr error
	result := placeholderRe.ReplaceAllStringFunc(tmpl, func(match string) string {
		key := placeholderRe.FindStringSubmatch(match)[1]
		val, ok := payload[key]
		if !ok {
			renderErr = fmt.Errorf("template variable %q not found in payload", key)
			return match
		}
		return fmt.Sprintf("%v", val)
	})
	if renderErr != nil {
		return "", renderErr
	}
	return result, nil
}
