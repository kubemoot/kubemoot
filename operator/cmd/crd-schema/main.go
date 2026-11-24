/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Command crd-schema writes the JSON Schemas of the Kubemoot CustomResourceDefinitions.
//
//	go run ./cmd/crd-schema -crds config/crd/bases -out dist/schemas
//
// The Publish Release workflow runs it on the release's source and attaches the files to
// the GitHub Release, which is where editors and CI read them.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/kubemoot/kubemoot/operator/pkg/crdschema"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "crd-schema:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("crd-schema", flag.ContinueOnError)
	crds := fs.String("crds", "config/crd/bases", "directory of the generated CustomResourceDefinitions")
	out := fs.String("out", "", "directory to write the schemas into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return fmt.Errorf("-out is required")
	}
	schemas, err := crdschema.ReadDir(*crds)
	if err != nil {
		return err
	}
	names, err := crdschema.WriteDir(schemas, *out)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, strings.Join(names, "\n"))
	return err
}
