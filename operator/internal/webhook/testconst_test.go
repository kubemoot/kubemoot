/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package webhook

// Shared fixtures for the webhook tests.
const (
	testCrewPrefix      = "crew-"
	testDNS1123Error    = "not DNS-1123 compliant"
	testFetchImage      = "mcp/fetch:latest"
	testGlobUnsupported = "glob patterns are not supported"
	testHealthCheck     = "discussion-health"
	testRepoURL         = "https://github.com/example/repo"
	testResourceName    = "test"
	testServerURL       = "http://some-server:8080"
)
