package zfs

import (
	"testing"
	"time"
)

// A zdap timestamp denotes the same instant however the host is configured:
// the layout carries no zone, so FormatTimestamp has to normalise to UTC and
// ParseTimestamp has to read it back as UTC. Nothing else in zdap may format
// or parse TimestampFormat directly.
func TestTimestampRoundTripIsZoneIndependent(t *testing.T) {
	instant := time.Date(2026, 11, 14, 13, 31, 0, 0, time.UTC)

	for _, tz := range []string{"UTC", "Europe/Stockholm", "America/Los_Angeles", "Asia/Tokyo"} {
		t.Run(tz, func(t *testing.T) {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				t.Skipf("zone %s not available: %v", tz, err)
			}

			// However the caller holds the instant, it is written the same way.
			if got, want := FormatTimestamp(instant.In(loc)), "2026-11-14T13.31.00"; got != want {
				t.Errorf("FormatTimestamp = %q, want %q", got, want)
			}

			back, err := ParseTimestamp(FormatTimestamp(instant.In(loc)))
			if err != nil {
				t.Fatal(err)
			}
			if !back.Equal(instant) {
				t.Errorf("round trip = %v, want %v", back, instant)
			}
		})
	}
}

// The comparison the clone pool makes in pruneExpired, which is what the whole
// convention exists to get right.
func TestExpiryComparisonHoldsInAnyZone(t *testing.T) {
	for _, tz := range []string{"UTC", "Europe/Stockholm", "Pacific/Auckland"} {
		t.Run(tz, func(t *testing.T) {
			loc, err := time.LoadLocation(tz)
			if err != nil {
				t.Skipf("zone %s not available: %v", tz, err)
			}
			prev := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = prev })

			past, err := ParseTimestamp(FormatTimestamp(time.Now().Add(-time.Minute)))
			if err != nil {
				t.Fatal(err)
			}
			if !past.Before(time.Now()) {
				t.Errorf("an expiry a minute ago is not seen as expired in %s", tz)
			}

			future, err := ParseTimestamp(FormatTimestamp(time.Now().Add(time.Hour)))
			if err != nil {
				t.Fatal(err)
			}
			if future.Before(time.Now()) {
				t.Errorf("an expiry an hour away is seen as expired in %s", tz)
			}
		})
	}
}
