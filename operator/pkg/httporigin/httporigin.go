/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package httporigin parses the list of browser origins a discussion gateway admits. The
// operator and the gateway both use it, so they agree on what a valid origin is.
package httporigin

import (
	"net/url"
	"strings"
)

// Valid reports whether s is an exact origin: an http or https scheme and a host, with no
// path, query, user or wildcard.
func Valid(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" &&
		u.Path == "" && u.RawQuery == "" && u.Fragment == "" && u.User == nil && !strings.Contains(s, "*")
}

// Parse splits a comma-separated list, trimming each entry and dropping blanks and
// repeats. It returns the valid origins and, separately, the entries it rejected.
func Parse(list string) (valid, rejected []string) {
	seen := map[string]bool{}
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		if Valid(entry) {
			valid = append(valid, entry)
		} else {
			rejected = append(rejected, entry)
		}
	}
	return valid, rejected
}
