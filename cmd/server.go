package cmd

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pathcl/pudu/api"
	"github.com/pathcl/pudu/vm"
)

func serverCmd(args []string) {
	cfg := vm.DefaultConfig()
	var port int

	fs := flag.NewFlagSet("server", flag.ExitOnError)
	fs.StringVar(&cfg.FirecrackerBin, "firecracker-bin", "", "path to firecracker binary")
	fs.StringVar(&cfg.KernelImagePath, "kernel", "", "path to vmlinux kernel image")
	fs.StringVar(&cfg.RootFSPath, "rootfs", "", "path to ext4 rootfs image")
	fs.StringVar(&cfg.CloudInitISO, "cloud-init-iso", "", "path to cloud-init ISO")
	fs.Int64Var(&cfg.VCPUs, "vcpus", cfg.VCPUs, "default vCPUs per VM")
	fs.Int64Var(&cfg.MemSizeMiB, "mem", cfg.MemSizeMiB, "default memory in MiB per VM")
	fs.Int64Var(&cfg.RootFSSizeMiB, "rootfs-size", cfg.RootFSSizeMiB, "rootfs size in MiB")
	fs.IntVar(&port, "port", 8888, "HTTP server port")
	fs.Parse(args) //nolint:errcheck

	srv := api.NewServer(cfg)

	mux := http.NewServeMux()

	// Mount REST API routes
	apiHandler := srv.Handler()
	mux.Handle("/api/", apiHandler)

	// Mount WebSSH terminal routes (/ and /ws) — vmCount starts at 0; the API manages VMs
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		// Delegate to the web terminal handler by temporarily mounting it
		// The web terminal is started per-fleet/scenario via the API; this is a
		// passthrough handler for any already-running web terminal mux.
		http.NotFound(w, r)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "<html><body><h1>pudu server</h1><p>Use the REST API at <a href=\"/api/v1/fleets\">/api/v1/fleets</a> and <a href=\"/api/v1/scenarios\">/api/v1/scenarios</a></p></body></html>")
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	fmt.Fprintf(os.Stderr, "==> pudu API server: http://localhost:%d\n", port)
	fmt.Fprintf(os.Stderr, "    REST API: http://localhost:%d/api/v1/\n", port)
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop\n\n")

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
