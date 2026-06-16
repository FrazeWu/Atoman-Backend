package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

var errCreateLogDir = errors.New("create log directory")

type loggingConfig struct {
	Dir    string
	Stdout io.Writer
	Stderr io.Writer
}

type serverLogs struct {
	AppFile     *os.File
	AccessFile  *os.File
	ErrorFile   *os.File
	FatalLogger *log.Logger
}

func resolveLogDir() string {
	if dir := strings.TrimSpace(os.Getenv("LOG_DIR")); dir != "" {
		return dir
	}
	return "./log"
}

func setupLogging(cfg loggingConfig) (*serverLogs, error) {
	if cfg.Stdout == nil {
		cfg.Stdout = os.Stdout
	}
	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}
	if cfg.Dir == "" {
		cfg.Dir = resolveLogDir()
	}

	if err := os.MkdirAll(cfg.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: %v", errCreateLogDir, err)
	}

	appFile, err := os.OpenFile(filepath.Join(cfg.Dir, "app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}

	accessFile, err := os.OpenFile(filepath.Join(cfg.Dir, "access.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = appFile.Close()
		return nil, err
	}

	errorFile, err := os.OpenFile(filepath.Join(cfg.Dir, "error.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		_ = accessFile.Close()
		_ = appFile.Close()
		return nil, err
	}

	logs := &serverLogs{
		AppFile:    appFile,
		AccessFile: accessFile,
		ErrorFile:  errorFile,
	}

	log.SetOutput(io.MultiWriter(cfg.Stdout, appFile))
	gin.DefaultWriter = io.MultiWriter(cfg.Stdout, accessFile)
	gin.DefaultErrorWriter = io.MultiWriter(cfg.Stderr, errorFile)
	logs.FatalLogger = log.New(io.MultiWriter(cfg.Stderr, errorFile), "", log.Flags())

	return logs, nil
}

func (s *serverLogs) Close() error {
	var err error
	if s == nil {
		return nil
	}
	if s.AppFile != nil {
		if closeErr := s.AppFile.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if s.AccessFile != nil {
		if closeErr := s.AccessFile.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if s.ErrorFile != nil {
		if closeErr := s.ErrorFile.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	return err
}
