package config

import (
	"time"
)

// Bool returns a pointer to the given bool.
func Bool(b bool) *bool {
	return &b
}

// BoolCopy returns a copy of the boolean pointer
func BoolCopy(b *bool) *bool {
	if b == nil {
		return nil
	}

	return Bool(*b)
}

// Uint returns a pointer to the given uint.
func Uint(i uint) *uint {
	return &i
}

// UintCopy returns a copy of the uint pointer
func UintCopy(i *uint) *uint {
	if i == nil {
		return nil
	}

	return Uint(*i)
}

// String returns a pointer to the given string.
func String(s string) *string {
	return &s
}

// StringCopy returns a copy of the string pointer
func StringCopy(s *string) *string {
	if s == nil {
		return nil
	}

	return String(*s)
}

// TimeDuration returns a pointer to the given time.Duration.
func TimeDuration(t time.Duration) *time.Duration {
	return &t
}

// TimeDurationCopy returns a copy of the time.Duration pointer
func TimeDurationCopy(t *time.Duration) *time.Duration {
	if t == nil {
		return nil
	}

	return TimeDuration(*t)
}
