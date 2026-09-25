//go:build cgo

package readline

/*
#cgo pkg-config: readline

#include <stdio.h>
#include <stdlib.h>
#include <readline/readline.h>
#include <readline/history.h>
#include <signal.h>

static volatile sig_atomic_t got_sigint = 0;

static struct sigaction old_sigint_action;
static int sigint_installed = 0;

static void native_sigint_handler(int signo) {
	(void)signo;
	got_sigint = 1;
}

static int sigint_event_hook(void) {
	if (got_sigint) {
		got_sigint = 0;

		rl_crlf();
		rl_on_new_line();
		rl_replace_line("", 0);
		rl_redisplay();

		return 0;
	}

	return 0;
}

static inline int setup_sigint_handler(void) {
	struct sigaction action;

	if (sigint_installed) {
		return 0;
	}

	memset(&action, 0, sizeof(action));
	action.sa_handler = native_sigint_handler;
	sigemptyset(&action.sa_mask);
	action.sa_flags = 0;

	if (sigaction(SIGINT, &action, &old_sigint_action) != 0) {
		return -1;
	}

	rl_event_hook = sigint_event_hook;
	sigint_installed = 1;

	return 0;
}

static inline int restore_sigint_handler(void) {
	if (!sigint_installed) {
		return 0;
	}

	rl_event_hook = NULL;
	sigint_installed = 0;
	got_sigint = 0;

	return sigaction(SIGINT, &old_sigint_action, NULL);
}

// use_tty_streams points readline's input/output streams directly at the
// controlling terminal instead of the process's stdin/stdout. This is
// required when stdin has been redirected (e.g. `echo prompt | agent -i`):
// without it, readline would read from the pipe, consume the piped bytes as
// a single line, and then hit EOF forever afterwards instead of falling back
// to the keyboard. Returns 0 on success, -1 if /dev/tty could not be opened.
static inline int use_tty_streams(void) {
	FILE *tty_in = fopen("/dev/tty", "r");
	if (tty_in == NULL) {
		return -1;
	}
	FILE *tty_out = fopen("/dev/tty", "w");
	if (tty_out == NULL) {
		fclose(tty_in);
		return -1;
	}

	rl_instream = tty_in;
	rl_outstream = tty_out;
	return 0;
}
*/
import "C"

import (
	"io"
	"os"
	"unsafe"

	"golang.org/x/term"
)

// If stdin has been piped/redirected (e.g. `echo prompt | agent -i`), point
// readline at the controlling terminal instead so interactive keyboard input
// keeps working after the piped data is consumed. Runs once at package init,
// before any ReadLine call.
func init() {
	C.rl_catch_signals = 0  /* don't install handlers for SIGINT/SIGTERM/SIGQUIT etc. */
	C.rl_catch_sigwinch = 0 /* don't handle terminal resize */

	if !term.IsTerminal(int(os.Stdin.Fd())) {
		if C.use_tty_streams() != 0 {
			panic("failed to open /dev/tty for readline")
		}
	}
}

// ReadLine calls GNU readline (the same library bash uses) with the given
// prompt string. ANSI colour codes in the prompt must be wrapped in
// \001...\002 so readline can calculate the visible prompt width correctly.
func ReadLine(prompt string) (string, error) {
	promptCStr := C.CString(prompt)
	defer C.free(unsafe.Pointer(promptCStr))

	C.setup_sigint_handler()
	line := C.readline(promptCStr)
	C.restore_sigint_handler()
	if line == nil {
		return "", io.EOF
	}
	defer C.free(unsafe.Pointer(line))

	return C.GoString(line), nil
}

func AddHistory(line string) {
	s := C.CString(line)
	defer C.free(unsafe.Pointer(s))
	C.add_history(s)
}
