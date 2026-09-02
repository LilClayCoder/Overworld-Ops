package docker

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// LogLine is one line of container output.
type LogLine struct {
	// Stream is "stdout" or "stderr".
	Stream string `json:"stream"`
	Text   string `json:"text"`
}

// Docker's multiplexed log frame header is 8 bytes: [stream type][3 padding
// bytes][4-byte big-endian payload length].
const logHeaderSize = 8

// DemuxLogs reads Docker's multiplexed log stream and delivers whole lines to
// out until the stream ends or ctx is cancelled. It always closes rc.
//
// Sending on out respects ctx, so a websocket client that disconnects mid-tail
// does not wedge this goroutine forever.
func DemuxLogs(ctx context.Context, rc io.ReadCloser, out chan<- LogLine) error {
	defer rc.Close()

	br := bufio.NewReader(rc)
	header := make([]byte, logHeaderSize)

	for {
		if _, err := io.ReadFull(br, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("read log header: %w", err)
		}

		streamName := "stdout"
		if header[0] == 2 {
			streamName = "stderr"
		}

		size := binary.BigEndian.Uint32(header[4:])
		if size == 0 {
			continue
		}

		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("read log payload: %w", err)
		}

		// A single frame can carry several lines, or a partial one. Splitting
		// on newline and dropping the trailing empty is good enough for a
		// console view; exact line reassembly across frames is not worth the
		// complexity here.
		for _, line := range strings.Split(strings.TrimRight(string(payload), "\n"), "\n") {
			select {
			case out <- LogLine{Stream: streamName, Text: line}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}
