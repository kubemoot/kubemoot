/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package httporigin

import (
	"reflect"
	"testing"
)

func TestParseKeepsValidOriginsAndReportsTheRest(t *testing.T) {
	valid, rejected := Parse(" https://a.example.com, ,*,https://a.example.com,http://localhost:5173," +
		"https://app.example.com/,https://*.example.com,app.example.com,ftp://x.example")
	if want := []string{"https://a.example.com", "http://localhost:5173"}; !reflect.DeepEqual(valid, want) {
		t.Errorf("valid = %v, want %v", valid, want)
	}
	want := []string{"*", "https://app.example.com/", "https://*.example.com", "app.example.com", "ftp://x.example"}
	if !reflect.DeepEqual(rejected, want) {
		t.Errorf("rejected = %v, want %v", rejected, want)
	}
}

func TestParseEmptyList(t *testing.T) {
	if valid, rejected := Parse(""); len(valid) != 0 || len(rejected) != 0 {
		t.Errorf("got %v, %v", valid, rejected)
	}
}
