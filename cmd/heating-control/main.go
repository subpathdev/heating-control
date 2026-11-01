// Command heating-control serves the phone page for the heating.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/subpathdev/heating-control/internal/config"
	"github.com/subpathdev/heating-control/internal/controller"
	"github.com/subpathdev/heating-control/internal/health"
	"github.com/subpathdev/heating-control/internal/mqtt"
	"github.com/subpathdev/heating-control/internal/web"
)

// version is set at build time.
var version = "dev"

func main() {
	configPath := flag.String("config", "/etc/heating-control/config.yaml", "path to the config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client := mqtt.New(cfg)
	ctrl := controller.New(cfg, client)
	client.Start(ctrl)
	defer client.Close()

	mux := http.NewServeMux()
	health.Register(mux, version, client.Checker())
	web.Register(mux, ctrl)

	srv := &http.Server{
		Addr: ":8080",
		// The page has no login, so refuse requests from other websites.
		Handler:           http.NewCrossOriginProtection().Handler(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}()

	log.Printf("heating-control %s listening on %s", version, srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
