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

package v1alpha1

// Spec fields that default to true are *bool so an explicit false is kept when
// the object is encoded again (a plain bool with omitempty would omit it, and the
// CRD default would apply). On the wire the field is a boolean or absent either
// way, so stored objects decode unchanged.

// BoolOrTrue reads a default-true *bool field: unset means true.
func BoolOrTrue(b *bool) bool {
	return b == nil || *b
}

// AdminUIEnabled reports whether the gateway admin interface is on (default true).
func (s *MCPGatewaySpec) AdminUIEnabled() bool {
	return BoolOrTrue(s.AdminUI)
}

// IsEnabled reports whether the discussion gateway is on (default true).
func (d *DiscussionConfig) IsEnabled() bool {
	return BoolOrTrue(d.Enabled)
}

// BlockBrokenEnabled reports whether "avoid" verdicts block a server (default true).
func (t *TestedConfig) BlockBrokenEnabled() bool {
	return BoolOrTrue(t.BlockBroken)
}
