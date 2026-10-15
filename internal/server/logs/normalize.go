// Package logs stores, searches and streams application log lines.
package logs

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"fleetwatch/internal/proto"
)

// Severities, from least to most severe; "unknown" when nothing tells us.
var Severities = []string{"debug", "info", "warning", "error", "critical", "unknown"}

// ANSI CSI/OSC sequences and other escape codes, so logs cannot restyle or
// spoof the viewer (terminal escape abuse).
var ansiRe = regexp.MustCompile(`\x1b(\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(\x07|\x1b\\)|[@-Z\\-_])`)

// Sanitize strips escape sequences and control characters (keeping tabs), fixes
// invalid UTF-8 and caps the length.
func Sanitize(s string, maxBytes int) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r == '\t' || r >= 32 && r != 127 && !(r >= 0x80 && r < 0xA0) {
			return r
		}
		return -1
	}, s)
	if len(s) > maxBytes {
		cut := maxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + " …[truncated]"
	}
	return s
}

// SanitizeOutput cleans command output for display: like Sanitize, but keeps line
// breaks and has no length cap.
func SanitizeOutput(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ToValidUTF8(s, "�")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' || r >= 32 && r != 127 && !(r >= 0x80 && r < 0xA0) {
			return r
		}
		return -1
	}, s)
}

// fromPriority maps a syslog priority (journald PRIORITY) to a severity.
func fromPriority(p int) string {
	switch {
	case p < 0:
		return ""
	case p <= 2:
		return "critical"
	case p == 3:
		return "error"
	case p == 4:
		return "warning"
	case p <= 6:
		return "info"
	}
	return "debug"
}

// fromLevel understands common logger level names and pino/bunyan numbers.
func fromLevel(v any) string {
	switch x := v.(type) {
	case float64:
		switch {
		case x >= 60:
			return "critical"
		case x >= 50:
			return "error"
		case x >= 40:
			return "warning"
		case x >= 30:
			return "info"
		default:
			return "debug"
		}
	case string:
		switch strings.ToLower(x) {
		case "trace", "debug", "verbose", "silly":
			return "debug"
		case "info", "notice", "http":
			return "info"
		case "warn", "warning":
			return "warning"
		case "error", "err":
			return "error"
		case "fatal", "critical", "crit", "panic", "emerg", "alert":
			return "critical"
		}
	}
	return ""
}

var (
	// Level words near the start of a line: "ERROR ...", "[warn] ...", "level=error", "2026-.. ERROR ...".
	levelWordRe = regexp.MustCompile(`(?i)(?:^|[\s\[\]|:(])(?:level=)?(trace|debug|info|notice|warn|warning|error|err|fatal|critical|crit|panic)(?:$|[\s\]\[|:)])`)
	exceptionRe = regexp.MustCompile(`^(Traceback \(most recent call last\)|panic: |Unhandled|Uncaught|\s+at .+\(.+:\d+:\d+\)$|[A-Za-z]+(Error|Exception)\b)`)
)

// Classify picks a severity. Structured metadata wins (journald priority, JSON
// level); words in the text are only a fallback, then the stream decides.
func Classify(l proto.LogLine, msg string) string {
	if s := fromPriority(l.Priority); s != "" {
		return s
	}
	if t := strings.TrimSpace(msg); strings.HasPrefix(t, "{") {
		var obj map[string]any
		if json.Unmarshal([]byte(t), &obj) == nil {
			for _, k := range []string{"level", "severity", "lvl", "levelname", "log.level"} {
				if s := fromLevel(obj[k]); s != "" {
					return s
				}
			}
		}
	}
	head := msg
	if len(head) > 120 {
		head = head[:120]
	}
	if m := levelWordRe.FindStringSubmatch(head); m != nil {
		if s := fromLevel(m[1]); s != "" {
			return s
		}
	}
	if exceptionRe.MatchString(msg) {
		return "error"
	}
	switch l.Stream {
	case "stdout":
		return "info"
	case "stderr":
		// Many runtimes (e.g. Python logging) write normal output to stderr.
		return "warning"
	}
	return "unknown"
}
