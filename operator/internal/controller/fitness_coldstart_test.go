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

package controller

import (
	"testing"
)

func TestBoolPtrDefaultTrue(t *testing.T) {
	tru, fls := true, false
	if !boolPtrDefaultTrue(nil) {
		t.Error("nil should default to true (matches CRD default)")
	}
	if !boolPtrDefaultTrue(&tru) {
		t.Error("&true should be true")
	}
	if boolPtrDefaultTrue(&fls) {
		t.Error("&false should be false")
	}
}
