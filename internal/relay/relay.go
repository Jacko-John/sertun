// Package relay transfers raw payloads while preserving cancellation and TCP
// half-close. Copy policies remain separate from connection ownership.
package relay

import (
	"context"
	"errors"
	"net"

	"sertun/internal/streamcopy"
)

// Bridge owns both connections until both directions finish or ctx is canceled.
func Bridge(ctx context.Context, a, b net.Conn) error {
	closeBoth := func() {
		a.Close()
		b.Close()
	}
	stop := context.AfterFunc(ctx, closeBoth)
	defer stop()
	defer closeBoth()
	copyDirection := func(dst, src net.Conn) error {
		_, err := streamcopy.Copy(dst, src)
		if err == nil {
			if writer, ok := dst.(interface{ CloseWrite() error }); ok {
				err = writer.CloseWrite()
			} else {
				closeBoth()
			}
		}
		if err != nil {
			closeBoth()
		}
		return err
	}
	result := make(chan error, 1)
	go func() { result <- copyDirection(a, b) }()
	err := copyDirection(b, a)
	other := <-result
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Join(err, other)
}
