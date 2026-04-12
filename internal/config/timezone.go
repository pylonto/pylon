package config

import (
	"os"
	"strings"
	"time"
)

// DetectSystemTimezone returns the IANA timezone name of the local system.
// Checks $TZ first, then /etc/localtime symlink, falls back to "UTC".
func DetectSystemTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	// On Linux, /etc/localtime is usually a symlink into /usr/share/zoneinfo.
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if idx := strings.Index(target, "zoneinfo/"); idx >= 0 {
			zone := target[idx+len("zoneinfo/"):]
			if _, err := time.LoadLocation(zone); err == nil {
				return zone
			}
		}
	}
	return "UTC"
}

// TimezoneList returns a curated list of common IANA timezone strings.
func TimezoneList() []string {
	return []string{
		"UTC",
		// Americas
		"America/New_York",
		"America/Chicago",
		"America/Denver",
		"America/Los_Angeles",
		"America/Anchorage",
		"America/Phoenix",
		"Pacific/Honolulu",
		"America/Toronto",
		"America/Vancouver",
		"America/Sao_Paulo",
		"America/Mexico_City",
		"America/Argentina/Buenos_Aires",
		"America/Bogota",
		"America/Lima",
		"America/Santiago",
		// Europe
		"Europe/London",
		"Europe/Paris",
		"Europe/Berlin",
		"Europe/Madrid",
		"Europe/Rome",
		"Europe/Amsterdam",
		"Europe/Zurich",
		"Europe/Stockholm",
		"Europe/Warsaw",
		"Europe/Prague",
		"Europe/Vienna",
		"Europe/Moscow",
		"Europe/Istanbul",
		"Europe/Athens",
		"Europe/Bucharest",
		"Europe/Helsinki",
		"Europe/Dublin",
		"Europe/Lisbon",
		// Asia
		"Asia/Tokyo",
		"Asia/Seoul",
		"Asia/Shanghai",
		"Asia/Hong_Kong",
		"Asia/Singapore",
		"Asia/Kolkata",
		"Asia/Dubai",
		"Asia/Jakarta",
		"Asia/Bangkok",
		"Asia/Taipei",
		"Asia/Manila",
		"Asia/Karachi",
		"Asia/Dhaka",
		"Asia/Riyadh",
		"Asia/Tehran",
		"Asia/Jerusalem",
		// Oceania
		"Australia/Sydney",
		"Australia/Melbourne",
		"Australia/Brisbane",
		"Australia/Perth",
		"Australia/Adelaide",
		"Pacific/Auckland",
		"Pacific/Fiji",
		// Africa
		"Africa/Cairo",
		"Africa/Lagos",
		"Africa/Johannesburg",
		"Africa/Nairobi",
		"Africa/Casablanca",
	}
}
