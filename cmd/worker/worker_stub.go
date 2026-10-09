//go:build !opencv

package main

import "fmt"

func run() error {
	return fmt.Errorf("worker requires go build -tags opencv")
}
