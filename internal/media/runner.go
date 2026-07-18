package media

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

const defaultFFmpegTimeout = 120 * time.Second

type LocalFFmpegRunner struct {
	Timeout time.Duration
}

func (runner LocalFFmpegRunner) Run(ctx context.Context, command FFmpegCommand) error {
	if err := validateFFmpegCommand(command); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := runner.Timeout
	if timeout <= 0 {
		timeout = defaultFFmpegTimeout
	}
	runContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	process := newLocalFFmpegCommand(runContext, command)
	if err := process.Start(); err != nil {
		return errors.Join(ErrFFmpegFailed, err)
	}
	done := make(chan error, 1)
	go func() {
		done <- process.Wait()
	}()
	select {
	case err := <-done:
		if err != nil {
			return errors.Join(ErrFFmpegFailed, err)
		}
		return nil
	case <-runContext.Done():
		terminateProcessGroup(process)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			if process.Process != nil {
				_ = process.Process.Kill()
			}
		}
		return runContext.Err()
	}
}

func newLocalFFmpegCommand(ctx context.Context, command FFmpegCommand) *exec.Cmd {
	process := exec.CommandContext(ctx, command.Executable, command.Args...)
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return process
}

func terminateProcessGroup(process *exec.Cmd) {
	if process == nil || process.Process == nil {
		return
	}
	pgid, err := syscall.Getpgid(process.Process.Pid)
	if err == nil {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		return
	}
	_ = process.Process.Kill()
}

func validateFFmpegCommand(command FFmpegCommand) error {
	if strings.TrimSpace(command.Executable) != "ffmpeg" {
		return ErrFFmpegExecutable
	}
	if len(command.Args) == 0 {
		return ErrFFmpegFailed
	}
	if !hasArgumentPair(command.Args, "-protocol_whitelist", "file") {
		return ErrUnsafeLocalPath
	}
	for _, argument := range command.Args {
		normalized := strings.ToLower(strings.TrimSpace(argument))
		for _, forbidden := range []string{"http:", "https:", "file:", "concat:", "crypto:", "data:", "tcp:", "udp:"} {
			if strings.Contains(normalized, forbidden) {
				return ErrUnsafeLocalPath
			}
		}
	}
	return nil
}

func hasArgumentPair(values []string, left string, right string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == left && values[index+1] == right {
			return true
		}
	}
	return false
}
