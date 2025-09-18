//go:build linux

package main

/*
#cgo linux LDFLAGS: -lasound
#include <alsa/asoundlib.h>

static void noalsa(const char *file, int line, const char *func, int err, const char *fmt, ...) {}
static void set_alsa_silent() { snd_lib_error_set_handler(noalsa); }
*/
import "C"

// silenceAlsa suppresses ALSA library error output on Linux to keep the UI clean.
func silenceAlsa() {
    C.set_alsa_silent()
}


