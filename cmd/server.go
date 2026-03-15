package cmd

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pathcl/pudu/api"
	"github.com/pathcl/pudu/vm"
	"github.com/pathcl/pudu/web"
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

	upgrader := websocket.Upgrader{
		CheckOrigin:     func(r *http.Request) bool { return true },
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	mux := http.NewServeMux()

	// REST API
	mux.Handle("/api/", srv.Handler())

	// WebSSH terminal — vmID validated against live fleet state
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		vmID, err := strconv.Atoi(r.URL.Query().Get("vm"))
		if err != nil || vmID < 0 {
			http.Error(w, "invalid vm id", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "websocket upgrade error: %v\n", err)
			return
		}
		defer conn.Close()
		WSHandler(r.Context(), conn, vmID)
	})

	// Web UI — served as-is; JS polls /api/v1/fleets for live VM list
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(web.IndexHTML) //nolint:errcheck
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

	fmt.Fprintf(os.Stderr, "==> pudu server:  http://localhost:%d        (web terminal)\n", port)
	fmt.Fprintf(os.Stderr, "    REST API:    http://localhost:%d/api/v1/\n", port)
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop\n\n")

	if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}
