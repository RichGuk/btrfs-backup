package main

import (
	"log"
	"os"
)

var errLog = log.New(os.Stderr, "[btrfs-backup] ", 0)
