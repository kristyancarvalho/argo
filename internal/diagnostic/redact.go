package diagnostic

import (
	"net/url"
	"regexp"
	"strings"
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
