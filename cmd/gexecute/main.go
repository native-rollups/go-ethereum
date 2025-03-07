// gexecute is a command-line tool for processing 'execute' verification requests via HTTP.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
	"github.com/ethereum/go-ethereum/nativerollup/execute"
	"github.com/ethereum/go-ethereum/params"
	"github.com/urfave/cli/v2"
	"go.uber.org/automaxprocs/maxprocs"
)

var app = cli.NewApp()

func init() {
	app.Name = "gexecute"
	app.Usage = "A binary for verifying native rollup 'execute' through re-execution"
	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:    "httpaddr",
			Value:   "127.0.0.1",
			Usage:   "HTTP listening address",
			EnvVars: []string{"GEXECUTE_HTTPADDR"},
		},
		&cli.IntFlag{
			Name:    "httpport",
			Value:   8555,
			Usage:   "HTTP listening port",
			EnvVars: []string{"GEXECUTE_HTTPPORT"},
		},
	}
	app.Action = gexecute
	app.Before = func(ctx *cli.Context) error {
		// Automatically set GOMAXPROCS to match Linux container CPU quota.
		if _, err := maxprocs.Set(); err != nil {
			log.Error("Failed to set maxprocs", "err", err)
		}
		return nil
	}
	app.After = func(ctx *cli.Context) error {
		// Any required cleanup can go here.
		return nil
	}
}

func main() {
	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// gexecute sets up and starts the HTTP server.
func gexecute(ctx *cli.Context) error {
	httpAddr := fmt.Sprintf("%s:%d", ctx.String("httpaddr"), ctx.Int("httpport"))
	log.Info("Starting gexecute server", "addr", httpAddr)

	// Register the /verifyV1 endpoint.
	http.HandleFunc("/verifyV1", handleVerify)

	// Create an HTTP server with basic timeouts.
	srv := &http.Server{
		Addr:         httpAddr,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	// Start the HTTP server; ListenAndServe blocks until the server exits.
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("HTTP server error", "err", err)
		return err
	}
	return nil
}

// handleVerify processes POST requests at the /verifyV1 endpoint.
func handleVerify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST method is allowed", http.StatusMethodNotAllowed)
		return
	}

	trace, err := io.ReadAll(r.Body)
	if err != nil {
		log.Error("Failed to read request body", "err", err)
		http.Error(w, "Failed to read request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	log.Info("Received /verifyV1 request")

	// Re-execute
	chainConfig := params.ChainConfig{}
	vmConfig := vm.Config{}
	execute.ExecutePrecompile(trace, &chainConfig, &vmConfig)

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Verification processed"))
}
