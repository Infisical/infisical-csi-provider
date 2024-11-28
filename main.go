package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/infisical/infisical-csi-provider/internal/server"
	"google.golang.org/grpc"
	pb "sigs.k8s.io/secrets-store-csi-driver/provider/v1alpha1"
)

var (
	hostUrl    = flag.String("host-url", "https://app.infisical.com", "infisical instance URL")
	endpoint   = flag.String("endpoint", "/tmp/infisical.socket", "Path to socket on which to listen for driver gRPC calls.")
	healthPort = flag.String("health-port", "8080", "Port for HTTP health check.")
)

// ListenAndServe accepts incoming connections on a Unix socket. It is a blocking method.
// Returns non-nil error unless Close or Shutdown is called.
func listenAndServe(gs *grpc.Server) error {
	if !strings.HasPrefix(*endpoint, "@") {
		err := os.Remove(*endpoint)
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to delete the socket file, error: %w", err)
		}
	}

	ln, err := net.Listen("unix", *endpoint)

	if err != nil {
		return err
	}
	defer ln.Close()

	s := &server.Server{
		HostUrl: *hostUrl,
	}

	pb.RegisterCSIDriverProviderServer(gs, s)

	log.Printf("Listening on socket: %s\n", *endpoint)

	return gs.Serve(ln)
}

func shutdown(gs *grpc.Server) {
	if gs != nil {
		gs.GracefulStop()
	}
}

func startHealthCheck() chan error {
	mux := http.NewServeMux()
	ms := http.Server{
		Addr:    fmt.Sprintf(":%s", *healthPort),
		Handler: mux,
	}

	errorCh := make(chan error)

	mux.HandleFunc("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	log.Printf("Initializing health check %+v", *healthPort)

	go func() {
		defer func() {
			err := ms.Shutdown(context.Background())
			if err != nil {
				log.Printf("error shutting down health handler: %+v", err)
			}
		}()

		select {
		case errorCh <- ms.ListenAndServe():
		default:
		}
	}()

	return errorCh
}

func startProviderServer() chan error {
	gs := grpc.NewServer(
		grpc.ConnectionTimeout(20 * time.Second),
	)

	errorCh := make(chan error)
	log.Println("Starting up provider server...")
	go func() {
		defer func() {
			shutdown(gs)
			close(errorCh)
		}()
		select {
		case errorCh <- listenAndServe(gs):
		default:
		}
	}()

	return errorCh
}

func realMain() error {
	signalsChan := make(chan os.Signal, 1)
	signal.Notify(signalsChan, syscall.SIGINT, syscall.SIGTERM)

	healthErrorChan := startHealthCheck()
	providerErrorChan := startProviderServer()

	for {
		select {
		case sig := <-signalsChan:
			return fmt.Errorf("captured %v, shutting down provider", sig)
		case providerErr := <-providerErrorChan:
			return providerErr
		case healthErr := <-healthErrorChan:
			return healthErr
		}
	}
}

func main() {
	flag.Parse()
	err := realMain()
	if err != nil {
		log.Printf("error running provider: %+v", err)
		os.Exit(1)
	}
}
