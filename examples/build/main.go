package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"oras.land/oras-go/v2/content/memory"

	"github.com/openotters/agentfile/build"
)

func main() {
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: build <Agentfile>")
		os.Exit(1)
	}

	path := args[0]

	target := memory.New()

	ref, err := build.FromFile(context.Background(), path, target)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("ref: %s\n", ref)

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if err := enc.Encode(ref); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
