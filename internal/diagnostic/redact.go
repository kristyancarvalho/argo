package diagnostic

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var urlPattern = regexp.MustCompile(`(?i)https?://[^\s"'<>]+`)

type redactedError struct {
	source error
}

func URL(value string) string {
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "[redacted URL]"
	}
	if parsed.User != nil {
		parsed.User = url.User("redacted")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		parsed.RawQuery = "redacted"
		parsed.ForceQuery = false
	}
	if parsed.Fragment != "" {
		parsed.Fragment = "redacted"
	}

	return parsed.String()
}

func Text(value string) string {
	return urlPattern.ReplaceAllStringFunc(value, func(candidate string) string {
		trimmed := strings.TrimRight(candidate, ").,;]:")
		suffix := candidate[len(trimmed):]

		return URL(trimmed) + suffix
	})
}

func Display(value string) string {
	var output strings.Builder
	for _, character := range value {
		switch character {
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		case '\a':
			output.WriteString(`\a`)
		default:
			if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
				if character <= 0xff {
					_, _ = fmt.Fprintf(&output, `\x%02x`, character)
				} else if character <= 0xffff {
					_, _ = fmt.Fprintf(&output, `\u%04x`, character)
				} else {
					_, _ = fmt.Fprintf(&output, `\U%08x`, character)
				}
				continue
			}
			output.WriteRune(character)
		}
	}

	return output.String()
}

func Filename(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) || unicode.Is(unicode.Cf, character) {
			return '_'
		}

		return character
	}, value)
	if strings.TrimSpace(value) == "" {
		return "download"
	}

	return value
}

func Error(source error) error {
	if source == nil {
		return nil
	}

	return redactedError{source: source}
}

func (err redactedError) Error() string {
	return Text(err.source.Error())
}

func (err redactedError) Unwrap() error {
	return err.source
}
