package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

func main() {
	Run(func(args string) (string, error) {
		if args == "" {
			return "", fmt.Errorf("usage: wget url")
		}

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, args, nil)
		if err != nil {
			return "", fmt.Errorf("creating request: %w", err)
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024))
		if err != nil {
			return "", err
		}

		return string(body), nil
	})
}
