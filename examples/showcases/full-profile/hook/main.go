package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	var (
		logPath = flag.String("log", "", "Path to append hook evidence.")
		event   = flag.String("event", "unknown", "Hook event name.")
	)
	flag.Parse()
	if *logPath == "" {
		fmt.Fprintln(os.Stderr, "hook log path is required")
		os.Exit(2)
	}
	if err := os.MkdirAll(filepath.Dir(*logPath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "create hook log dir: %v\n", err)
		os.Exit(1)
	}
	line := fmt.Sprintf("%s %s PROFILE_HOOK_DEMO_OK\n", time.Now().UTC().Format(time.RFC3339Nano), *event)
	file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open hook log: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()
	if _, err := file.WriteString(line); err != nil {
		fmt.Fprintf(os.Stderr, "write hook log: %v\n", err)
		os.Exit(1)
	}
}
