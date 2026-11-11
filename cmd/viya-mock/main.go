// Command viya-mock serves an in-memory fake of the SAS Viya REST APIs, for
// trying the provider by hand without a Viya deployment.
//
//	go run ./cmd/viya-mock
//
// It keeps its state in memory only: everything is lost when it stops.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jacoblamarche/terraform-provider-viya/internal/viyamock"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	requireIfMatch := flag.Bool("require-if-match", false, "reject updates that have no If-Match header")
	flag.Parse()

	if err := run(*addr, *requireIfMatch); err != nil {
		log.Fatalf("viya-mock: %v", err)
	}
}

func run(addr string, requireIfMatch bool) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var opts []viyamock.Option
	if requireIfMatch {
		opts = append(opts, viyamock.WithRequireIfMatch())
	}

	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Handler:           viyamock.New(opts...),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("viya-mock listening on http://%s", listener.Addr())
	log.Printf("  client: %s / %s", viyamock.DefaultClientID, viyamock.DefaultClientSecret)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
