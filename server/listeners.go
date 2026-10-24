package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

func listenHTTP(addresses []string) ([]net.Listener, error) {
	listeners := make([]net.Listener, 0, len(addresses))
	for _, address := range addresses {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			// A partial bind must never leave the service partially available.
			for _, opened := range listeners {
				_ = opened.Close()
			}
			return nil, fmt.Errorf("listen on %s: %w", address, err)
		}
		listeners = append(listeners, listener)
	}
	return listeners, nil
}

func serveHTTP(ctx context.Context, srv *http.Server, listeners []net.Listener, timeout time.Duration, report func(error)) error {
	results := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func(listener net.Listener) {
			err := srv.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) {
				results <- nil
				return
			}
			results <- fmt.Errorf("serve on %s: %w", listener.Addr(), err)
		}(listener)
	}
	remaining := len(listeners)
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-results:
		remaining--
		if serveErr != nil {
			report(serveErr)
		}
	}
	// Wait for draining before run releases shared databases and other resources.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	shutdownErr := srv.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		shutdownErr = fmt.Errorf("http shutdown: %w", shutdownErr)
		report(shutdownErr)
		if err := srv.Close(); err != nil {
			report(fmt.Errorf("http force close: %w", err))
		}
	}
	for ; remaining > 0; remaining-- {
		if err := <-results; err != nil {
			report(err)
			serveErr = errors.Join(serveErr, err)
		}
	}
	return errors.Join(serveErr, shutdownErr)
}
