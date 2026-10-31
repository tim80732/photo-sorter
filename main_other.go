//go:build !windows

package main

import (
	"os"
	_ "time/tzdata" // the Docker image has no zoneinfo; TZ comes from Unraid
)

func runDefault() { os.Exit(cmdServe(nil)) }
