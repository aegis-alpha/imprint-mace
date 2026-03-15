package db

import (
	"crypto/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

// NewID generates a new ULID. Chronologically sortable, 26 characters.
func NewID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()
}
