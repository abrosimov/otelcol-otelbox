package contract

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

const (
	openMarker     = "# >>> SHARED"
	closeMarker    = "# <<< SHARED"
	expectedBlocks = 3
)

type sharedProfile struct {
	path    string
	content []byte
}

func CheckSharedFiles(paths []string, output io.Writer) error {
	if len(paths) < 2 {
		return fmt.Errorf("shared check needs at least two profiles, got %d", len(paths))
	}

	profiles := make([]sharedProfile, 0, len(paths))
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open profile %s: %w", path, err)
		}
		content, extractErr := extractShared(file, path)
		closeErr := file.Close()
		if extractErr != nil {
			return extractErr
		}
		if closeErr != nil {
			return fmt.Errorf("close profile %s: %w", path, closeErr)
		}
		profiles = append(profiles, sharedProfile{path: path, content: content})
	}

	reference := profiles[0]
	referenceDigest := sha256.Sum256(reference.content)
	fmt.Fprintf(output, "shared check: %s  %x  reference\n", reference.path, referenceDigest)

	for _, profile := range profiles[1:] {
		digest := sha256.Sum256(profile.content)
		if !bytes.Equal(reference.content, profile.content) {
			line, expected, actual := firstDifference(reference.content, profile.content)
			return fmt.Errorf(
				"shared regions in %s diverge from %s at extracted line %d: expected %q, got %q",
				profile.path,
				reference.path,
				line,
				expected,
				actual,
			)
		}
		fmt.Fprintf(output, "shared check: %s  %x  ok\n", profile.path, digest)
	}

	fmt.Fprintf(output, "shared check: %d profiles agree on %d shared regions\n", len(profiles), expectedBlocks)
	return nil
}

func extractShared(input io.Reader, path string) ([]byte, error) {
	scanner := bufio.NewScanner(input)
	var content bytes.Buffer
	region := 0
	inBlock := false
	openCount := 0
	closeCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case bytes.Contains([]byte(line), []byte(openMarker)):
			if inBlock {
				return nil, fmt.Errorf("profile %s has nested shared markers", path)
			}
			inBlock = true
			region++
			openCount++
			fmt.Fprintf(&content, "%s %d\n", openMarker, region)
		case bytes.Contains([]byte(line), []byte(closeMarker)):
			if !inBlock {
				return nil, fmt.Errorf("profile %s has an out-of-order closing shared marker", path)
			}
			inBlock = false
			closeCount++
			fmt.Fprintf(&content, "%s %d\n", closeMarker, region)
		case inBlock:
			content.WriteString(line)
			content.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read profile %s: %w", path, err)
	}
	if inBlock {
		return nil, fmt.Errorf("profile %s has an unclosed shared region", path)
	}
	if openCount != expectedBlocks || closeCount != expectedBlocks {
		return nil, fmt.Errorf(
			"profile %s has %d opening and %d closing markers, expected %d of each",
			path,
			openCount,
			closeCount,
			expectedBlocks,
		)
	}
	if content.Len() == 0 {
		return nil, fmt.Errorf("profile %s yielded empty shared regions", path)
	}

	return content.Bytes(), nil
}

func firstDifference(expected, actual []byte) (int, string, string) {
	expectedLines := bytes.Split(expected, []byte{'\n'})
	actualLines := bytes.Split(actual, []byte{'\n'})
	limit := min(len(expectedLines), len(actualLines))
	for index := range limit {
		if !bytes.Equal(expectedLines[index], actualLines[index]) {
			return index + 1, string(expectedLines[index]), string(actualLines[index])
		}
	}
	if len(expectedLines) > limit {
		return limit + 1, string(expectedLines[limit]), "<missing>"
	}
	return limit + 1, "<missing>", string(actualLines[limit])
}
