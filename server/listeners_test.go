package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestListenHTTPRollsBackPartialBind(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := probe.Addr().String()
	probe.Close()
	if _, err := listenHTTP([]string{address, address}); err == nil {
		t.Fatal("expected duplicate bind failure")
	}
	probe, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("partial bind leaked: %v", err)
	}
	probe.Close()
}

func TestServeHTTPMultipleListeners(t *testing.T) {
	listeners, err := listenHTTP([]string{"127.0.0.1:0", "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	srv := newHTTPServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	go func() {
		done <- serveHTTP(ctx, srv, listeners, time.Second, func(err error) { t.Errorf("unexpected error: %v", err) })
	}()
	client := &http.Client{Timeout: time.Second}
	for _, listener := range listeners {
		response, err := client.Get("http://" + listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNoContent {
			t.Fatal(response.Status)
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown blocked")
	}
	for _, listener := range listeners {
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err == nil {
			conn.Close()
			t.Fatal("listener remained open")
		}
	}
}

func TestServeHTTPListenerFailure(t *testing.T) {
	listeners, err := listenHTTP([]string{"127.0.0.1:0", "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	listeners[0].Close()
	reported := 0
	err = serveHTTP(context.Background(), newHTTPServer("", http.NewServeMux()), listeners, time.Second, func(error) { reported++ })
	if err == nil || reported == 0 {
		t.Fatal("expected reported listener failure")
	}
	conn, err := net.DialTimeout("tcp", listeners[1].Addr().String(), time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("other listener remained open")
	}
}

func TestServeHTTPShutdownTimeout(t *testing.T) {
	listeners, err := listenHTTP([]string{"127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	started, released, exited := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer close(released)
	srv := newHTTPServer("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-released:
		}
		close(exited)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	reported := make(chan error, 2)
	go func() {
		done <- serveHTTP(ctx, srv, listeners, 20*time.Millisecond, func(err error) { reported <- err })
	}()
	go func() {
		client := &http.Client{Timeout: time.Second}
		if response, err := client.Get("http://" + listeners[0].Addr().String()); err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected timeout")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked")
	}
	select {
	case <-reported:
	default:
		t.Fatal("timeout not reported")
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("connection not force closed")
	}
}
