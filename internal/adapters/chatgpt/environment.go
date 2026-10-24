package chatgpt

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"unicode/utf8"
)

type environmentValue struct {
	Value   string
	Present bool
}

type targetEnvironment struct {
	Home, CodexHome environmentValue
}

var errProcessEnvironment = errors.New("chatgpt: process environment unavailable or unsupported")

// KERN_PROCARGS2 contains argc, the executable path, padding, argv and envp.
// Skip argv without converting it to strings and retain only the two path
// fields. The caller must clear the original buffer immediately after parsing.
func parseProcessEnvironment(data []byte, expectedExecutable string) (targetEnvironment, error) {
	if len(data) < 5 || len(data) > 1<<20 || !filepath.IsAbs(expectedExecutable) {
		return targetEnvironment{}, errProcessEnvironment
	}
	argc := int(binary.LittleEndian.Uint32(data[:4]))
	if argc < 1 || argc > len(data)-4 {
		return targetEnvironment{}, errProcessEnvironment
	}
	remaining := data[4:]
	next := func() ([]byte, bool) {
		end := bytes.IndexByte(remaining, 0)
		if end < 0 {
			return nil, false
		}
		value := remaining[:end]
		remaining = remaining[end+1:]
		return value, true
	}
	executable, ok := next()
	if !ok || !bytes.Equal(executable, []byte(expectedExecutable)) {
		return targetEnvironment{}, errProcessEnvironment
	}
	for len(remaining) > 0 && remaining[0] == 0 {
		remaining = remaining[1:]
	}
	for i := 0; i < argc; i++ {
		if _, ok := next(); !ok {
			return targetEnvironment{}, errProcessEnvironment
		}
	}
	var result targetEnvironment
	for {
		entry, ok := next()
		if !ok {
			return targetEnvironment{}, errProcessEnvironment
		}
		if len(entry) == 0 {
			return result, nil
		}
		var field *environmentValue
		var value []byte
		switch {
		case bytes.HasPrefix(entry, []byte("HOME=")):
			field, value = &result.Home, entry[len("HOME="):]
		case bytes.HasPrefix(entry, []byte("CODEX_HOME=")):
			field, value = &result.CodexHome, entry[len("CODEX_HOME="):]
		default:
			continue
		}
		if field.Present || len(value) > 4096 || !utf8.Valid(value) {
			return targetEnvironment{}, errProcessEnvironment
		}
		*field = environmentValue{Value: string(value), Present: true}
	}
}

func configHomeFromEnvironment(env targetEnvironment) (string, error) {
	if env.CodexHome.Present {
		if !filepath.IsAbs(env.CodexHome.Value) {
			return "", errProcessEnvironment
		}
		return filepath.Clean(env.CodexHome.Value), nil
	}
	if !env.Home.Present || !filepath.IsAbs(env.Home.Value) {
		return "", errProcessEnvironment
	}
	return filepath.Join(env.Home.Value, ".codex"), nil
}
