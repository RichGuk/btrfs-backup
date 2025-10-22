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
	lastBytes    int64
	startTime    time.Time
	lastUpdate   time.Time
	mu           sync.Mutex
	label        string
	updateTicker *time.Ticker
	done         chan bool
}

func NewProgressWriter(output io.Writer, label string) *ProgressWriter {
	now := time.Now()
	pw := &ProgressWriter{
		output:       output,
		startTime:    now,
		lastUpdate:   now,
		label:        label,
		updateTicker: time.NewTicker(time.Second),
		done:         make(chan bool),
	}

	go pw.displayLoop()

	return pw
}

func (pw *ProgressWriter) displayLoop() {
	for {
		select {
		case <-pw.done:
			return
		case now := <-pw.updateTicker.C:
			pw.mu.Lock()
			elapsed := now.Sub(pw.startTime)

			bytesSinceLastUpdate := pw.bytesWritten - pw.lastBytes
			instantRate := float64(bytesSinceLastUpdate)
			pw.lastBytes = pw.bytesWritten

			var status string
			if instantRate > 0 {
				status = fmt.Sprintf("%s/s", formatBytes(int64(instantRate)))
			} else if pw.bytesWritten > 0 {
				status = "stalled"
			} else {
				status = "waiting..."
			}

			_, _ = fmt.Fprintf(
				pw.output,
				"\r\033[K→ %s: %s transferred, %s, %s elapsed",
				pw.label,
				formatBytes(pw.bytesWritten),
				status,
				formatDuration(elapsed),
			)
			pw.mu.Unlock()
		}
	}
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n := len(p)

	pw.mu.Lock()
	pw.bytesWritten += int64(n)
	pw.mu.Unlock()

	return n, nil
}

func (pw *ProgressWriter) Finish() {
	pw.updateTicker.Stop()
	pw.done <- true

	pw.mu.Lock()
	defer pw.mu.Unlock()

	elapsed := time.Since(pw.startTime)
	avgBytesPerSec := float64(pw.bytesWritten) / elapsed.Seconds()

	_, _ = fmt.Fprintf(
		pw.output,
		"\r\033[K→ %s: %s transferred, %s/s average, %s total\n",
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
