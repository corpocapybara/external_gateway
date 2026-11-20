package main

import (
	"io"
	"os"
	"path/filepath"

	"github.com/external_gateway/internal/svclog"
	"github.com/rs/zerolog"
)

// serviceLogPath is the file egw's operational log is also written to (EG0065):
// none for an interactive run (console, -install, -remove), where stderr is
// visible; under the SCM the configured log.file, else
// <ProgramData>/external_gateway/logs/<svc-name>.log.
func serviceLogPath(interactive bool, svcName, override string) string {
	if interactive {
		return ""
	}
	if override != "" {
		return override
	}
	return filepath.Join(programData(), "external_gateway", "logs", svcName+".log")
}

func programData() string {
	if d := os.Getenv("ProgramData"); d != "" {
		return d
	}
	return `C:\ProgramData`
}

// newLogger builds the operational logger. With a path it writes to stderr and
// to a svclog.Writer on that path; the file is opened with retention 0, so
// nothing is pruned before the config is read. When the file cannot be opened
// the logger still works (stderr only) and says so; a later Switch can still
// open another path. An empty path gives a stderr-only logger and a nil writer.
func newLogger(path string, stderr io.Writer) (zerolog.Logger, *svclog.Writer) {
	if path == "" {
		return zerolog.New(stderr).With().Timestamp().Caller().Logger(), nil
	}
	w := svclog.New()
	logger := zerolog.New(zerolog.MultiLevelWriter(stderr, w)).With().Timestamp().Caller().Logger()
	if err := w.Switch(path, 0); err != nil {
		warnLogFile(logger, path, err)
	}
	return logger, w
}

func warnLogFile(logger zerolog.Logger, path string, err error) {
	logger.Warn().Err(err).Str("path", path).Msg("service log file unavailable")
}
