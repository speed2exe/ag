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

static void native_sigint_handler(int signo) {
	(void)signo;
	got_sigint = 1;
}

// Ctrl+C while readline owns the terminal should clear the current line rather
// than kill the process. The handler only records the signal; the redraw has to
// happen from readline's event hook, outside of signal context.
static int sigint_event_hook(void) {
	if (!got_sigint) {
		return 0;
	}
	got_sigint = 0;

	rl_crlf();
	rl_on_new_line();
	rl_replace_line("", 0);
	rl_redisplay();

	return 0;
}

static void setup_sigint_handler(void) {
	struct sigaction action = {0};

	action.sa_handler = native_sigint_handler;
	sigemptyset(&action.sa_mask);

	sigaction(SIGINT, &action, &old_sigint_action);
	rl_event_hook = sigint_event_hook;
}

static void restore_sigint_handler(void) {
	rl_event_hook = NULL;
	got_sigint = 0;
	sigaction(SIGINT, &old_sigint_action, NULL);
}

*/
import "C"

import (
	"io"
	"unsafe"
)

// ReadLine is only ever called in interactive mode, where stdin and stdout are
// already the terminal. Stop readline from installing its own signal and resize
// handlers; SIGINT is handled by setup_sigint_handler instead.
func init() {
	C.rl_catch_signals = 0  /* don't install handlers for SIGINT/SIGTERM/SIGQUIT etc. */
	C.rl_catch_sigwinch = 0 /* don't handle terminal resize */
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
