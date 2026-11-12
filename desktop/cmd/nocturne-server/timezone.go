package main

import (
	"os"
	"time"
)

// Go on Android always starts in UTC, so the Android app passes the phone's
// time zone (for archive names and hourly copies).
func init() {
	if tz := os.Getenv("NOCTURNE_TZ"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			time.Local = loc
		}
	}
}
