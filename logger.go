package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

func SetupLogger(cfg LogConfig) (*log.Logger, io.Closer, error) {
	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create log dir: %w", err)
	}

	filename := fmt.Sprintf("%s-%s.log", cfg.FilePrefix, time.Now().Format("20060102"))
	path := filepath.Join(cfg.Dir, filename)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("open log file: %w", err)
	}

	writer := io.MultiWriter(os.Stdout, file)
	logger := log.New(writer, "[qq-summary-bot] ", log.LstdFlags|log.Lmsgprefix)
	return logger, file, nil
}
