package translate

import (
	"strings"

	"github.com/tidwall/gjson"
)

// parseDataURL extracts mime type and base64 data from a data:{mime};base64,{data} URL.
// Returns ok=false if the URL doesn't match that pattern. Base64 content is not validated.
func parseDataURL(url string) (mime, data string, ok bool) {
	if !strings.HasPrefix(url, "data:") {
		return "", "", false
	}
	rest := strings.TrimPrefix(url, "data:")
	mime, data, ok = strings.Cut(rest, ";base64,")
	if !ok || data == "" {
		return "", "", false
	}
	return mime, data, true
}

// toolResultContentGJSON flattens tool result content to a single string.
func toolResultContentGJSON(content gjson.Result) string {
	switch content.Type {
	case gjson.String:
		return content.String()
	case gjson.JSON:
		if !content.IsArray() {
			return ""
		}
		var parts []string
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" {
				if text := block.Get("text").String(); text != "" {
					parts = append(parts, text)
				}
			}
			return true
		})
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}
