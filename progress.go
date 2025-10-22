package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

type ProgressWriter struct {
	output       io.Writer
	bytesWritten int64
	startTime    time.Time
	lastUpdate   time.Time
	mu           sync.Mutex
	label        string
}

func NewProgressWriter(output io.Writer, label string) *ProgressWriter {
	now := time.Now()
	return &ProgressWriter{
		output:     output,
		startTime:  now,
		lastUpdate: now,
		label:      label,
	}
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n := len(p)

	pw.mu.Lock()
	pw.bytesWritten += int64(n)
	now := time.Now()

	if now.Sub(pw.lastUpdate) >= time.Second {
		pw.lastUpdate = now
		elapsed := now.Sub(pw.startTime)
		bytesPerSec := float64(pw.bytesWritten) / elapsed.Seconds()

		_, _ = fmt.Fprintf(
			pw.output,
			"\r→ %s: %s transferred, %s/s, %s elapsed",
			pw.label,
			formatBytes(pw.bytesWritten),
			formatBytes(int64(bytesPerSec)),
			formatDuration(elapsed),
		)
	}

	pw.mu.Unlock()

	return n, nil
}

func (pw *ProgressWriter) Finish() {
	pw.mu.Lock()
	defer pw.mu.Unlock()

	elapsed := time.Since(pw.startTime)
	avgBytesPerSec := float64(pw.bytesWritten) / elapsed.Seconds()

	_, _ = fmt.Fprintf(
		pw.output,
		"\r→ %s: %s transferred, %s/s average, %s total\n",
		pw.label,
		formatBytes(pw.bytesWritten),
		formatBytes(int64(avgBytesPerSec)),
		formatDuration(elapsed),
	)
}

func formatBytes(bytes int64) string {
	const unit = 1000
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second

	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	} else if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
