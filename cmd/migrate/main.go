package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	path := filepath.Join("migrations", "001_init.sql")
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read migration: %v\n", err)
		os.Exit(1)
	}

	fmt.Print(string(data))
}
