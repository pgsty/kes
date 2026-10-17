//go:build ignore

// Copyright 2026 - PGSTY contributors.
// Use of this source code is governed by the AGPLv3 license in LICENSE.

package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run buildscripts/gen-ldflags.go RFC3339-TIME")
		os.Exit(1)
	}
	t, err := time.Parse(time.RFC3339, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tag := "RELEASE." + t.UTC().Format("2006-01-02T15-04-05Z")
	fmt.Println("-s -w -X github.com/minio/kes/internal/sys.releaseVersion=" + tag)
}
