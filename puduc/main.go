// puduc is a CLI client for the pudu REST API server.
//
// Usage:
//
//	puduc fleet create --count 2
//	puduc fleet list
//	puduc fleet delete <id>
//	puduc scenario run <file.yaml> [--scale web=2]
//	puduc scenario status <id>
//	puduc scenario hint <id>
//	puduc scenario abort <id>
//	puduc terminal <vm-id>
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

var serverURL string

func main() {
	flag.StringVar(&serverURL, "server", envOrDefault("PUDU_SERVER", "http://localhost:8888"), "pudu server URL")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	switch args[0] {
	case "fleet":
		fleetCmd(args[1:])
	case "scenario":
		scenarioCmd(args[1:])
	case "terminal":
		terminalCmd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Usage: puduc [--server URL] <command> [args]

Environment:
  PUDU_SERVER   server URL (default: http://localhost:8888)

Commands:
  fleet create [--count N] [--kernel K] [--rootfs R] [--cloud-init-iso I] [--mem M] [--vcpus V]
  fleet list
  fleet delete <id>

  scenario run <file.yaml> [--scale web=2,db=1] [--web-port P]
  scenario status <id>
  scenario hint <id>
  scenario abort <id>

  terminal <vm-id>   open web terminal URL for VM
`)
}

// ── Fleet ──────────────────────────────────────────────────────────────────

func fleetCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "fleet subcommands: create, list, delete\n")
		os.Exit(1)
	}
	switch args[0] {
	case "create":
		fleetCreate(args[1:])
	case "list":
		fleetList()
	case "delete":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "fleet delete <id>\n")
			os.Exit(1)
		}
		fleetDelete(args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown fleet subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func fleetCreate(args []string) {
	fs := flag.NewFlagSet("fleet create", flag.ExitOnError)
	count := fs.Int("count", 1, "number of VMs")
	kernel := fs.String("kernel", "", "kernel image path")
	rootfs := fs.String("rootfs", "", "rootfs image path")
	iso := fs.String("cloud-init-iso", "", "cloud-init ISO path")
	mem := fs.Int64("mem", 0, "memory in MiB (0 = server default)")
	vcpus := fs.Int64("vcpus", 0, "vCPUs (0 = server default)")
	fs.Parse(args) //nolint:errcheck

	body := map[string]interface{}{
		"count":          *count,
		"kernel":         *kernel,
		"rootfs":         *rootfs,
		"cloud_init_iso": *iso,
		"mem_mb":         *mem,
		"vcpus":          *vcpus,
	}
	resp := apiPost("/api/v1/fleets", body)
	printJSON(resp)
}

func fleetList() {
	resp := apiGet("/api/v1/fleets")
	printJSON(resp)
}

func fleetDelete(id string) {
	apiDelete("/api/v1/fleets/" + id)
	fmt.Printf("fleet %s deleted\n", id)
}

// ── Scenario ───────────────────────────────────────────────────────────────

func scenarioCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "scenario subcommands: run, status, hint, abort\n")
		os.Exit(1)
	}
	switch args[0] {
	case "run":
		scenarioRun(args[1:])
	case "status":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "scenario status <id>\n")
			os.Exit(1)
		}
		scenarioStatus(args[1])
	case "hint":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "scenario hint <id>\n")
			os.Exit(1)
		}
		scenarioHint(args[1])
	case "abort":
		if len(args) < 2 {
			fmt.Fprintf(os.Stderr, "scenario abort <id>\n")
			os.Exit(1)
		}
		scenarioAbort(args[1])
	default:
		fmt.Fprintf(os.Stderr, "unknown scenario subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func scenarioRun(args []string) {
	fs := flag.NewFlagSet("scenario run", flag.ExitOnError)
	scale := fs.String("scale", "", "tier scale overrides e.g. web=2,db=1")
	webPort := fs.Int("web-port", 8888, "WebSSH terminal port")
	kernel := fs.String("kernel", "", "kernel image path")
	rootfs := fs.String("rootfs", "", "rootfs image path")
	iso := fs.String("cloud-init-iso", "", "cloud-init ISO path")
	fs.Parse(args) //nolint:errcheck

	scenarioFile := fs.Arg(0)
	if scenarioFile == "" {
		fmt.Fprintf(os.Stderr, "scenario run <file.yaml>\n")
		os.Exit(1)
	}

	body := map[string]interface{}{
		"scenario_file":  scenarioFile,
		"scale":          *scale,
		"web_port":       *webPort,
		"kernel":         *kernel,
		"rootfs":         *rootfs,
		"cloud_init_iso": *iso,
	}
	resp := apiPost("/api/v1/scenarios", body)
	printJSON(resp)
}

func scenarioStatus(id string) {
	resp := apiGet("/api/v1/scenarios/" + id)
	printJSON(resp)
}

func scenarioHint(id string) {
	resp := apiPost("/api/v1/scenarios/"+id+"/hint", nil)
	printJSON(resp)
}

func scenarioAbort(id string) {
	apiDelete("/api/v1/scenarios/" + id)
	fmt.Printf("scenario %s aborted\n", id)
}

// ── Terminal ───────────────────────────────────────────────────────────────

func terminalCmd(args []string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "terminal <vm-id>\n")
		os.Exit(1)
	}
	vmID := args[0]
	url := fmt.Sprintf("%s/ws?vm=%s", serverURL, vmID)
	// Strip ws scheme for display
	webURL := strings.Replace(serverURL, "http://", "http://", 1) + "/?vm=" + vmID
	fmt.Printf("Web terminal: %s\n", webURL)
	fmt.Printf("WebSocket:    %s\n", url)
}

// ── HTTP helpers ───────────────────────────────────────────────────────────

func apiGet(path string) []byte {
	resp, err := http.Get(serverURL + path) //nolint:noctx
	if err != nil {
		fatal("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fatal("GET %s: %s\n%s", path, resp.Status, data)
	}
	return data
}

func apiPost(path string, body interface{}) []byte {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	} else {
		r = bytes.NewReader([]byte("{}"))
	}
	resp, err := http.Post(serverURL+path, "application/json", r) //nolint:noctx
	if err != nil {
		fatal("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		fatal("POST %s: %s\n%s", path, resp.Status, data)
	}
	return data
}

func apiDelete(path string) {
	req, _ := http.NewRequest(http.MethodDelete, serverURL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fatal("DELETE %s: %v", path, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		fatal("DELETE %s: %s", path, resp.Status)
	}
}

func printJSON(data []byte) {
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		fmt.Println(string(data))
		return
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
