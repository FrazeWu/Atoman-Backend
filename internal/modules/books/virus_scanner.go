package books

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

const bookVirusScanTimeout = 5 * time.Minute

var ErrBookVirusDetected = errors.New("book virus detected")

type bookVirusScanner interface {
	Scan(context.Context, io.Reader) error
}

type clamAVBookVirusScanner struct {
	binary string
}

func NewClamAVBookVirusScanner(binary string) (bookVirusScanner, error) {
	binary = strings.TrimSpace(binary)
	if binary == "" {
		return nil, nil
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return nil, fmt.Errorf("clamav executable is unavailable: %w", err)
	}
	return &clamAVBookVirusScanner{binary: resolved}, nil
}

func (scanner *clamAVBookVirusScanner) Scan(ctx context.Context, reader io.Reader) error {
	if reader == nil {
		return fmt.Errorf("virus scan input is empty")
	}
	scanCtx, cancel := context.WithTimeout(ctx, bookVirusScanTimeout)
	defer cancel()
	command := exec.CommandContext(scanCtx, scanner.binary, "--no-summary", "--stdout", "-")
	command.Stdin = reader
	command.Stdout = io.Discard
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return fmt.Errorf("%w: %s", ErrBookVirusDetected, message)
		}
		return fmt.Errorf("clamav scan failed: %s", message)
	}
	return nil
}
