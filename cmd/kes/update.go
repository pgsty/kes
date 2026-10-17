// Copyright 2021 - MinIO, Inc. All rights reserved.
// Copyright 2026 - PGSTY contributors.
// Use of this source code is governed by the AGPLv3
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"

	"github.com/minio/kes/internal/cli"
)

func updateCmd(args []string) {
	const message = "PGSTY KES does not self-update. Upgrade through your package manager, container image, or a verified release from https://github.com/pgsty/kes/releases."
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprintln(os.Stdout, message)
		return
	}
	cli.Fatal(message)
}
