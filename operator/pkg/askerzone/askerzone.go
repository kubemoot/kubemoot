/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package askerzone validates the IANA time zone a client sends with a question. Both
// doors of a crew (the discussion gateway and the crew liaison) use it, so a zone is
// accepted or dropped the same way wherever the question enters. The zone database is
// embedded, so the answer does not depend on the image the door runs in.
package askerzone

import (
	"strings"
	"time"
	_ "time/tzdata" // the zone database travels with the binary
)

// maxLength bounds a zone name before it is looked up; the longest IANA names are under 40.
const maxLength = 64

// Valid returns the zone trimmed, and true, when it names a zone of the IANA time zone
// database. An empty name, "Local", a name that is too long and one the database does not
// know are all invalid, so a bad client value behaves like an absent one.
func Valid(zone string) (string, bool) {
	zone = strings.TrimSpace(zone)
	if zone == "" || len(zone) > maxLength || zone == "Local" {
		return "", false
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return "", false
	}
	return zone, true
}
