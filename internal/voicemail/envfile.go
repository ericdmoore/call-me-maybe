package voicemail

import (
	"os"
	"strings"
)

// ReadEnvFile parses /etc/asterisk/cmm-mail.env, which began life as a file
// a shell script sourced and so may be written the way shell is written:
// `export KEY=value`, single or double quotes, a trailing ` # comment` after
// an unquoted value. Nothing is expanded and nothing is executed. A missing
// file is an empty map; any other read error is returned, so the caller can
// say the file exists but this user cannot read it.
func ReadEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			continue
		}
		out[key] = shellValue(strings.TrimSpace(value))
	}
	return out, nil
}

func shellValue(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}
