/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package identity

import (
	"errors"
	"net/url"
	"strings"
)

// schemeHTTPS is the only scheme a policy decision point or service is called with.
const schemeHTTPS = "https"

// EnvAskersPolicyHosts is the install's list of policy decision point hosts a crew's external
// askers rule may use, comma separated. The operator reads it from its own environment and
// passes it to each gateway, so the crew author who wrote the URL is not the one who decides
// where each asker's identity is sent. Empty trusts no host: the external kind is refused.
const EnvAskersPolicyHosts = "ACCESS_ASKERS_POLICY_HOSTS"

// ErrPolicyHostNotTrusted means the external rule's host is not on the install's list.
var ErrPolicyHostNotTrusted = errors.New("the policy decision point's host is not one this install trusts")

// PolicyHosts are the host names (no scheme, port or path) an install trusts as policy
// decision points, lower case.
type PolicyHosts []string

// ParsePolicyHosts reads a comma separated list. An entry that is not a plain host name (it has
// a scheme, port, path, wildcard or space) is returned in rejected and never trusted.
func ParsePolicyHosts(raw string) (hosts PolicyHosts, rejected []string) {
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		host := strings.ToLower(strings.TrimSuffix(item, "."))
		if host == "" || strings.ContainsAny(host, "/:*@?# \t") {
			rejected = append(rejected, item)
			continue
		}
		hosts = append(hosts, host)
	}
	return hosts, rejected
}

// Trusts reports whether rawURL is an https address whose host is on the list.
func (h PolicyHosts) Trusts(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != schemeHTTPS || u.User != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return false
	}
	for _, t := range h {
		if t == host {
			return true
		}
	}
	return false
}
