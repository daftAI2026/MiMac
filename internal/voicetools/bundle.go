package voicetools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const bundleFieldTimeout = 2 * time.Second

type Bundle struct {
	Path       string
	Identifier string
	Version    string
	Build      string
}

// InspectBundle extracts only the bundle identity and version fields from
// Contents/Info.plist. It does not return or log the rest of the plist.
func InspectBundle(ctx context.Context, appPath string) (Bundle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, err
	}
	plistPath := filepath.Join(appPath, "Contents", "Info.plist")
	info, err := os.Stat(plistPath)
	if errors.Is(err, os.ErrNotExist) {
		return Bundle{}, ErrBundleMetadata
	}
	if err != nil {
		return Bundle{}, ErrFileRead
	}
	if !info.Mode().IsRegular() {
		return Bundle{}, ErrBundleMetadata
	}
	file, err := os.Open(plistPath)
	if err != nil {
		return Bundle{}, ErrFileRead
	}
	if err := file.Close(); err != nil {
		return Bundle{}, ErrFileRead
	}

	identifier, err := extractPlistString(ctx, plistPath, "CFBundleIdentifier")
	if err != nil {
		return Bundle{}, err
	}
	version, err := extractPlistString(ctx, plistPath, "CFBundleShortVersionString")
	if err != nil {
		return Bundle{}, err
	}
	build, err := extractPlistString(ctx, plistPath, "CFBundleVersion")
	if err != nil {
		return Bundle{}, err
	}
	if identifier == "" || version == "" || build == "" {
		return Bundle{}, ErrBundleMetadata
	}
	return Bundle{Path: appPath, Identifier: identifier, Version: version, Build: build}, nil
}

func extractPlistString(ctx context.Context, plistPath, field string) (string, error) {
	switch field {
	case "CFBundleIdentifier", "CFBundleShortVersionString", "CFBundleVersion":
	default:
		return "", ErrBundleMetadata
	}
	commandCtx, cancel := context.WithTimeout(ctx, bundleFieldTimeout)
	defer cancel()

	cmd := exec.CommandContext(commandCtx, "/usr/bin/plutil", "-extract", field, "raw", "-o", "-", plistPath)
	var output limitedBuffer
	output.limit = 256
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		if contextErr := commandCtx.Err(); contextErr != nil {
			return "", contextErr
		}
		var startErr *exec.Error
		if errors.As(err, &startErr) {
			return "", ErrFileRead
		}
		return "", ErrBundleMetadata
	}
	value := strings.TrimSpace(output.buffer.String())
	if output.exceeded || !utf8.ValidString(value) || value == "" {
		return "", ErrBundleMetadata
	}
	return value, nil
}

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		b.exceeded = true
		return 0, ErrBundleMetadata
	}
	return b.buffer.Write(data)
}

// FindBundle checks exact candidate paths and verifies each bundle identifier.
// A user and system copy are both considered; two valid matches are ambiguous.
func FindBundle(ctx context.Context, candidates []string, expectedIdentifier string) (Bundle, State, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Bundle{}, ReadError, err
	}
	if expectedIdentifier == "" {
		return Bundle{}, Unsupported, nil
	}

	seen := make(map[string]struct{}, len(candidates))
	matches := make([]Bundle, 0, 1)
	var firstExisting string
	var metadataErr error
	for _, path := range candidates {
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		if err := ctx.Err(); err != nil {
			return Bundle{}, ReadError, err
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Bundle{Path: path}, ReadError, ErrFileRead
		}
		if firstExisting == "" {
			firstExisting = path
		}
		if !info.IsDir() {
			continue
		}
		bundle, err := InspectBundle(ctx, path)
		if err != nil {
			if ctx.Err() != nil {
				return Bundle{}, ReadError, ctx.Err()
			}
			metadataErr = err
			continue
		}
		if bundle.Identifier == expectedIdentifier {
			matches = append(matches, bundle)
		}
	}

	if len(matches) > 1 {
		return Bundle{}, Ambiguous, nil
	}
	if metadataErr != nil {
		if errors.Is(metadataErr, ErrBundleMetadata) {
			return Bundle{}, Unsupported, nil
		}
		return Bundle{}, ReadError, ErrFileRead
	}
	if len(matches) == 1 {
		return matches[0], Ready, nil
	}
	if firstExisting != "" {
		return Bundle{Path: firstExisting}, Unsupported, nil
	}
	return Bundle{}, NotFound, nil
}
