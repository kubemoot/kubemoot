/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package controller

import (
	"fmt"
	"strconv"

	"github.com/xuri/excelize/v2"

	kubemootv1alpha1 "github.com/kubemoot/kubemoot/operator/api/v1alpha1"
)

// sheetRealm is the report tab that records what the run ran on.
const sheetRealm = "Realm"

// sectionDigest heads the digest rows of the Realm tab.
const sectionDigest = "digest"

// realmRow is one line of the Realm tab: a section, an item in it, and its detail.
type realmRow struct{ section, item, detail string }

// realmRows flattens status.realm into report rows: the digests, the drift, then the
// start and end snapshots. A suite with no realm gets one row that says so, since a
// result without its realm cannot be compared.
func realmRows(realm *kubemootv1alpha1.FitnessRealm) []realmRow {
	if realm == nil {
		return []realmRow{{"realm", "recorded", "no: this run did not record the realm it ran on"}}
	}
	var rows []realmRow
	if cf := realm.CopiedFrom; cf != nil {
		rows = append(rows, realmRow{"realm", "copied from", fmt.Sprintf("%s run %s (the answers ran there)", cf.Suite, cf.RunID)})
	}
	if d := realm.Digests; d != nil {
		rows = append(rows,
			realmRow{sectionDigest, "servers", d.Servers}, realmRow{sectionDigest, "models", d.Models},
			realmRow{sectionDigest, "versions", d.Versions}, realmRow{sectionDigest, "resources", d.Resources})
	}
	for _, d := range realm.Drift {
		rows = append(rows, realmRow{"drift", "start to end", d})
	}
	rows = append(rows, snapshotRows("start", realm.Start)...)
	return append(rows, snapshotRows("end", realm.End)...)
}

func snapshotRows(section string, s *kubemootv1alpha1.RealmSnapshot) []realmRow {
	if s == nil {
		return nil
	}
	rows := []realmRow{
		{section, "observed at", s.ObservedAt.UTC().Format("2006-01-02 15:04:05")},
		{section, "operator version", s.OperatorVersion},
		{section, "crew version", s.CrewVersion},
		{section, "other suites running", fmt.Sprintf("%v", s.ConcurrentSuites)},
		{section, "other standalone runs", strconv.Itoa(int(s.ConcurrentRuns))},
	}
	for _, m := range s.ModelServers {
		rows = append(rows, serverRows(section, m)...)
	}
	for _, a := range s.Agents {
		rows = append(rows, realmRow{section, "agent container " + a.Container, fmt.Sprintf(
			"image %s; %d pod(s); cpu request %s limit %s; memory request %s limit %s",
			a.Image, a.Pods, a.CPURequest, a.CPULimit, a.MemoryRequest, a.MemoryLimit)})
	}
	for _, n := range s.ResourceNotes {
		rows = append(rows, realmRow{section, "resource note", n})
	}
	return rows
}

func serverRows(section string, m kubemootv1alpha1.RealmModelServer) []realmRow {
	detail := fmt.Sprintf("%s %s; ready %t", m.Engine, m.EngineVersion, m.Ready)
	if a := m.Accelerator; a != nil {
		detail += fmt.Sprintf("; accelerator memory %d MiB (%d in use), %d parallel, context %d",
			a.MemoryMiB, a.MemoryUsedMiB, a.MaxParallel, a.ContextLength)
	}
	rows := make([]realmRow, 0, 1+len(m.Models))
	rows = append(rows, realmRow{section, "model server " + m.Namespace + "/" + m.Name, detail})
	for _, mod := range m.Models {
		state := "not resident"
		if mod.Resident {
			state = "resident"
		}
		rows = append(rows, realmRow{section, "model " + mod.Name, fmt.Sprintf(
			"on %s; %s; digest %s; quantization %s; context %d", m.Name, state, mod.Digest, mod.Quantization, mod.ContextLength)})
	}
	return rows
}

// writeRealmSheet writes the Realm tab from the suite status.
func writeRealmSheet(f *excelize.File, suite *kubemootv1alpha1.CrewFitnessSuite) error {
	rows := append([]realmRow{{"section", "item", "detail"}}, realmRows(suite.Status.Realm)...)
	for i, r := range rows {
		for col, v := range []string{r.section, r.item, r.detail} {
			if err := f.SetCellValue(sheetRealm, cellAt(col+1, i+1), v); err != nil {
				return err
			}
		}
	}
	_ = f.SetColWidth(sheetRealm, "A", "A", 12)
	_ = f.SetColWidth(sheetRealm, "B", "B", 40)
	_ = f.SetColWidth(sheetRealm, "C", "C", 110)
	return nil
}

// realm writes the Overview summary of the realm: the digests a reader compares
// between runs, and how many things drifted during this one.
func (o *overviewWriter) realm(suite *kubemootv1alpha1.CrewFitnessSuite) error {
	r := suite.Status.Realm
	if r == nil {
		return o.put("Realm", "not recorded: this run cannot be compared to another")
	}
	if d := r.Digests; d != nil {
		if err := o.put("Realm digests", fmt.Sprintf("servers %s, models %s, versions %s, resources %s (Realm tab)",
			d.Servers, d.Models, d.Versions, d.Resources)); err != nil {
			return err
		}
	}
	if len(r.Drift) > 0 {
		return o.put("Realm drift", fmt.Sprintf("%d change(s) between start and end: the system moved during the run", len(r.Drift)))
	}
	return nil
}
