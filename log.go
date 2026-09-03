package main

import (
	"os"
	"strings"
	"time"

	// The IANA timezone database, compiled into the binary.
	//
	// The image is FROM scratch, so there is no /usr/share/zoneinfo for the
	// runtime to read: TZ=Europe/Copenhagen is accepted and then silently
	// ignored, and every timestamp comes out UTC. Two hours wrong in a log is
	// worse than obviously wrong, because it reads as correct.
	//
	// ~450KB embedded, against carrying a tzdata package and the base image to
	// install it with.
	_ "time/tzdata"

	"github.com/rs/zerolog"
)

// newLogger builds the process-wide logger using ConsoleWriter -- a coloured,
// human-readable "TIME LEVEL message key=value" line rather than a raw
// structured dump. Level comes from LOG_LEVEL.
//
// Call sites are expected to build a complete, readable sentence and pass it
// as the message. What a log line here has to answer is "which file, and what
// happened to it" -- and since the outcomes include deleting a file and
// emailing it to a device, the sentence has to say which of those it was. A
// bag of fields does not.
func newLogger(levelStr string) zerolog.Logger {
	level, err := zerolog.ParseLevel(strings.ToLower(levelStr))
	if err != nil {
		level = zerolog.InfoLevel
	}

	// Go reads TZ at first use rather than on a change, and zerolog formats
	// with time.Now() in the local zone -- so this only has to be right once,
	// here, before anything is logged.
	if tz := os.Getenv("TZ"); tz != "" {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			// Reported rather than silently falling back to UTC, which is how
			// this went unnoticed in the first place.
			boot := zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout})
			boot.Warn().Msgf("TZ is set to %q, which is not a timezone this build knows — timestamps will be UTC", tz)
		} else {
			time.Local = loc
		}
	}

	writer := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: "15:04:05",
		// NoColor left false deliberately: stdout is a pipe in a container,
		// but `docker logs` and the viewers actually used against this stack
		// render ANSI fine.
	}

	return zerolog.New(writer).Level(level).With().Timestamp().Logger()
}
