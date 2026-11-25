// Package log provides simple levelled debug logging to a file.
// Logging is a no-op unless --logging-enabled is set; all functions are safe
// to call at any time without checking the flag first.
package log

import (
	"fmt"
	"log"
	"os"
)

var enabled bool

// Init opens (or creates) the log file at filepath and enables logging.
// It is a no-op when filepath is empty.
func Init(filepath string) error {
	if filepath == "" {
		enabled = false
		return nil
	}
	f, err := os.Create(filepath)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	log.SetOutput(f)
	enabled = true
	return nil
}

// Infof logs a formatted message at INFO level. It is a no-op when logging is disabled.
func Infof(format string, v ...any) {
	if enabled {
		log.Printf("[INFO] "+format, v...)
	}
}

// Warnf logs a formatted message at WARN level. It is a no-op when logging is disabled.
func Warnf(format string, v ...any) {
	if enabled {
		log.Printf("[WARN] "+format, v...)
	}
}

// Errorf logs a formatted message at ERROR level. It is a no-op when logging is disabled.
func Errorf(format string, v ...any) {
	if enabled {
		log.Printf("[ERROR] "+format, v...)
	}
}
