package cmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pathcl/pudu/vm"
	"github.com/pathcl/pudu/web"
	"golang.org/x/crypto/ssh"
)

func serveCmd(args []string) {
	cfg := vm.DefaultConfig()
	var count int
	var port int
	var cloudInitConfig string

	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	fs.StringVar(&cfg.FirecrackerBin, "firecracker-bin", "", "path to firecracker binary (overrides FIRECRACKER_BIN env and PATH)")
	fs.StringVar(&cfg.SocketPath, "socket", cfg.SocketPath, "Firecracker API socket path")
	fs.StringVar(&cfg.KernelImagePath, "kernel", "", "path to uncompressed vmlinux kernel image (required)")
	fs.StringVar(&cfg.RootFSPath, "rootfs", "", "path to ext4 root filesystem image (required)")
	fs.Int64Var(&cfg.VCPUs, "vcpus", cfg.VCPUs, "number of vCPUs")
	fs.Int64Var(&cfg.MemSizeMiB, "mem", cfg.MemSizeMiB, "memory in MiB")
	fs.Int64Var(&cfg.RootFSSizeMiB, "rootfs-size", cfg.RootFSSizeMiB, "rootfs size in MiB (0 = no resize)")
	fs.StringVar(&cfg.KernelArgs, "kernel-args", cfg.KernelArgs, "kernel boot arguments")
	fs.StringVar(&cfg.TapDeviceName, "tap", "", "TAP device name for networking (device must be pre-created)")
	fs.StringVar(&cfg.MacAddress, "mac", "", "MAC address for the VM network interface")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level (Trace, Debug, Info, Warn, Error)")
	fs.StringVar(&cfg.CloudInitISO, "cloud-init-iso", "", "path to cloud-init NoCloud ISO image (base path for multi-VM)")
	fs.StringVar(&cloudInitConfig, "cloud-init-config", "cloud-init-config.yaml", "path to cloud-init config template")
	fs.IntVar(&count, "count", 1, "number of VMs to launch")
	fs.IntVar(&port, "port", 8080, "HTTP server port")
	fs.Parse(args) //nolint:errcheck

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	if count > 1 && cfg.CloudInitISO != "" {
		if err := generatePerVMCloudInitISOs(cfg.CloudInitISO, cloudInitConfig, count); err != nil {
			fmt.Fprintf(os.Stderr, "error generating cloud-init ISOs: %v\n", err)
			os.Exit(1)
		}
	}

	go func() {
		if err := launchFleet(ctx, cfg, count); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "fleet error: %v\n", err)
		}
	}()

	StartWebTerminal(ctx, count, port)
}

// StartWebTerminal starts the WebSSH HTTP server and blocks until ctx is cancelled.
func StartWebTerminal(ctx context.Context, vmCount, port int) {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		html := strings.ReplaceAll(string(web.IndexHTML), "__VM_COUNT__", strconv.Itoa(vmCount))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(html)) //nolint:errcheck
	})

	upgrader := websocket.Upgrader{
		CheckOrigin:     func(r *http.Request) bool { return true },
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		vmID, err := strconv.Atoi(r.URL.Query().Get("vm"))
		if err != nil || vmID < 0 || vmID >= vmCount {
			http.Error(w, "invalid vm id", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "websocket upgrade error: %v\n", err)
			return
		}
		defer conn.Close()
		wsHandler(ctx, conn, vmID)
	})

	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx) //nolint:errcheck
	}()

	fmt.Fprintf(os.Stderr, "\n==> WebSSH server: http://192.168.50.100:%d\n", port)
	fmt.Fprintf(os.Stderr, "Press Ctrl+C to stop\n\n")

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func wsHandler(ctx context.Context, conn *websocket.Conn, vmID int) {
	sshAddr := fmt.Sprintf("172.16.%d.2:22", vmID)

	sendError := func(msg string) {
		type errorMsg struct {
			Type string `json:"type"`
			Msg  string `json:"msg"`
		}
		conn.WriteJSON(errorMsg{Type: "error", Msg: msg}) //nolint:errcheck
	}

	var sshConn *ssh.Client
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(2 * time.Second)
		}
		sshConn, err = ssh.Dial("tcp", sshAddr, &ssh.ClientConfig{
			User:            "root",
			Auth:            []ssh.AuthMethod{ssh.Password("root")},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         5 * time.Second,
		})
		if err == nil {
			break
		}
	}
	if err != nil {
		sendError(fmt.Sprintf("failed to connect to VM: %v", err))
		return
	}
	defer sshConn.Close()

	sess, err := sshConn.NewSession()
	if err != nil {
		sendError(fmt.Sprintf("failed to create session: %v", err))
		return
	}
	defer sess.Close()

	if err := sess.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		sendError(fmt.Sprintf("failed to request PTY: %v", err))
		return
	}

	stdin, err := sess.StdinPipe()
	if err != nil {
		sendError(fmt.Sprintf("failed to get stdin: %v", err))
		return
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		sendError(fmt.Sprintf("failed to get stdout: %v", err))
		return
	}
	stderr, err := sess.StderrPipe()
	if err != nil {
		sendError(fmt.Sprintf("failed to get stderr: %v", err))
		return
	}

	if err := sess.Shell(); err != nil {
		sendError(fmt.Sprintf("failed to start shell: %v", err))
		return
	}

	mu := &sync.Mutex{}
	wsWrite := func(data []byte) {
		mu.Lock()
		conn.WriteMessage(websocket.BinaryMessage, data) //nolint:errcheck
		mu.Unlock()
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			if n > 0 {
				wsWrite(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				wsWrite(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var msg map[string]interface{}
		if json.Unmarshal(data, &msg) == nil {
			if mType, _ := msg["type"].(string); mType == "resize" {
				cols, _ := msg["cols"].(float64)
				rows, _ := msg["rows"].(float64)
				sess.WindowChange(int(rows), int(cols)) //nolint:errcheck
			}
			continue
		}
		stdin.Write(data) //nolint:errcheck
	}
}
