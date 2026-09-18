// Package log provides simple levelled debug logging to a file.
// Logging is a no-op unless --logging-enabled is set; all functions are safe
// to call at any time without checking the flag first.
package log

import (
	"fmt"
	"log"
	"os"
)

var (
	enabled bool
)

// Init opens (or creates) the log file specified by arg.V.LogFilePath when
// logging is enabled, and redirects the standard logger output to that file.
// It is a no-op when logging is disabled.
func Init(filepath *string) error {
	if filepath != nil {
		f, err := os.Create(*filepath)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		log.SetOutput(f)
		enabled = true
	} else {
		enabled = false
	}
	return nil
}

// Debugf logs a formatted message at DEBUG level. It is a no-op when logging is disabled.
func Debugf(format string, v ...any) {
	if enabled {
		log.Printf("[DEBUG] "+format, v...)
	}
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

// Fatalf logs a formatted message at FATAL level and terminates the process.
// Unlike the other log functions, this always runs regardless of the logging flag.
func Fatalf(format string, v ...any) {
	log.Fatalf("[FATAL] "+format, v...)
}
