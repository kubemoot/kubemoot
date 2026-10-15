/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// go-jsonschema writes struct fields in name order. encoding/json encodes fields in
// declaration order, so the generator puts them back in the order the schema lists
// the properties: an event encodes with its keys in the documented order.

// jsonName finds the JSON name in a generated field's tag.
var jsonName = regexp.MustCompile("`json:\"([^,\"]+)")

// PropertyOrders reads a schema file and returns, for every schema in it with a
// title and properties, the property names in the order the file lists them.
func PropertyOrders(data []byte) (map[string][]string, error) {
	orders := map[string][]string{}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := walkValue(dec, orders); err != nil {
		return nil, err
	}
	return orders, nil
}

// walkValue reads one JSON value from dec, recording property orders of objects in it.
func walkValue(dec *json.Decoder, orders map[string][]string) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch tok {
	case json.Delim('{'):
		return walkObject(dec, orders)
	case json.Delim('['):
		for dec.More() {
			if err := walkValue(dec, orders); err != nil {
				return err
			}
		}
		_, err = dec.Token()
		return err
	default:
		return nil
	}
}

// walkObject reads the rest of an object whose opening brace dec already read.
func walkObject(dec *json.Decoder, orders map[string][]string) error {
	var title string
	var props []string
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return err
		}
		switch key {
		case "title":
			err = dec.Decode(&title)
		case "properties":
			props, err = propertyNames(dec, orders)
		default:
			err = walkValue(dec, orders)
		}
		if err != nil {
			return err
		}
	}
	if title != "" && len(props) > 0 {
		orders[title] = props
	}
	_, err := dec.Token()
	return err
}

// propertyNames reads a properties object, returning its keys in order and walking
// each property's schema.
func propertyNames(dec *json.Decoder, orders map[string][]string) ([]string, error) {
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("properties is not an object (%v)", err)
	}
	var names []string
	for dec.More() {
		key, err := objectKey(dec)
		if err != nil {
			return nil, err
		}
		names = append(names, key)
		if err := walkValue(dec, orders); err != nil {
			return nil, err
		}
	}
	_, err := dec.Token()
	return names, err
}

// objectKey reads the next key of an object.
func objectKey(dec *json.Decoder) (string, error) {
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	key, ok := tok.(string)
	if !ok {
		return "", fmt.Errorf("unexpected token %v", tok)
	}
	return key, nil
}

// ReorderFields rewrites every generated struct named in orders so its fields follow
// the listed JSON names. go-jsonschema separates fields with a blank line, each with
// its doc comment above it.
func ReorderFields(src string, orders map[string][]string) string {
	for name, order := range orders {
		src = reorderStruct(src, name, order)
	}
	return src
}

// reorderStruct reorders the fields of struct name in src.
func reorderStruct(src, name string, order []string) string {
	open := "type " + name + " struct {\n"
	start := strings.Index(src, open)
	if start < 0 {
		return src
	}
	bodyStart := start + len(open)
	end := strings.Index(src[bodyStart:], "\n}\n")
	if end < 0 {
		return src
	}
	fields := strings.Split(src[bodyStart:bodyStart+end], "\n\n")
	rank := map[string]int{}
	for i, n := range order {
		rank[n] = i
	}
	sort.SliceStable(fields, func(i, j int) bool {
		return fieldRank(fields[i], rank) < fieldRank(fields[j], rank)
	})
	return src[:bodyStart] + strings.Join(fields, "\n\n") + src[bodyStart+end:]
}

// fieldRank is the position of a field's JSON name in the schema, or after every
// listed property when it is not listed.
func fieldRank(field string, rank map[string]int) int {
	m := jsonName.FindStringSubmatch(field)
	if m == nil {
		return len(rank)
	}
	if r, ok := rank[m[1]]; ok {
		return r
	}
	return len(rank)
}
