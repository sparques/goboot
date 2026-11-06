package goboot

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// OpenLogger opens the configured destination before boot changes the root.
// The caller must close the returned closer after logging has stopped.
func OpenLogger(output string) (*slog.Logger, io.Closer, error) {
	if output == "" {
		output = DefaultLogOutput
	}
	var writer io.Writer
	var closer io.Closer = io.NopCloser(strings.NewReader(""))
	switch output {
	case "stdout":
		writer = os.Stdout
	case "stderr":
		writer = os.Stderr
	default:
		if err := absoluteCleanPath("logging.output", output); err != nil {
			return nil, nil, err
		}
		flags := os.O_WRONLY | os.O_APPEND
		if !strings.HasPrefix(output, "/dev/") {
			flags |= os.O_CREATE
		}
		file, err := os.OpenFile(output, flags, 0600)
		if err != nil {
			return nil, nil, fmt.Errorf("open log output %q: %w", output, err)
		}
		writer = file
		closer = file
	}
	return slog.New(slog.NewTextHandler(writer, nil)), closer, nil
}
