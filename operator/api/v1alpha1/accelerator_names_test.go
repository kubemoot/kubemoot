/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package v1alpha1

import "testing"

func TestAcceleratorMemoryPrefersTheNewFieldAndFallsBack(t *testing.T) {
	for name, tc := range map[string]struct {
		spec ModelSpec
		want int32
	}{
		"neither":  {ModelSpec{}, 0},
		"new only": {ModelSpec{AcceleratorMemoryMiB: 8000}, 8000},
		"old only": {ModelSpec{VRAMMib: 9000}, 9000},
		"both":     {ModelSpec{AcceleratorMemoryMiB: 8000, VRAMMib: 9000}, 8000},
	} {
		if got := tc.spec.AcceleratorMemory(); got != tc.want {
			t.Errorf("%s: AcceleratorMemory() = %d, want %d", name, got, tc.want)
		}
		entry := CatalogModel{AcceleratorMemoryMiB: tc.spec.AcceleratorMemoryMiB, VRAMMib: tc.spec.VRAMMib}
		if got := entry.AcceleratorMemory(); got != tc.want {
			t.Errorf("%s: catalog AcceleratorMemory() = %d, want %d", name, got, tc.want)
		}
	}
}

const (
	oldName = "old"
	newName = "new"
)

func TestCapacityAccessorsPreferTheNewFieldAndFallBack(t *testing.T) {
	var none *DiscoveredCapacity
	if none.TotalMiB() != 0 || none.UsedMiB() != 0 || none.Accelerator() != "" {
		t.Errorf("a nil capacity reads as unknown")
	}
	old := &DiscoveredCapacity{VRAMTotalMiB: 100, VRAMUsedMiB: 10, GPUModel: oldName}
	if old.TotalMiB() != 100 || old.UsedMiB() != 10 || old.Accelerator() != oldName {
		t.Errorf("an older status reads through the old names: %d %d %q", old.TotalMiB(), old.UsedMiB(), old.Accelerator())
	}
	both := &DiscoveredCapacity{
		AcceleratorMemoryTotalMiB: 200, AcceleratorMemoryUsedMiB: 20, AcceleratorModel: newName,
		VRAMTotalMiB: 100, VRAMUsedMiB: 10, GPUModel: oldName,
	}
	if both.TotalMiB() != 200 || both.UsedMiB() != 20 || both.Accelerator() != newName {
		t.Errorf("the new names win: %d %d %q", both.TotalMiB(), both.UsedMiB(), both.Accelerator())
	}
}

func TestLoadedModelAcceleratorBytes(t *testing.T) {
	if got := (LoadedModel{SizeVRAM: 5}).AcceleratorBytes(); got != 5 {
		t.Errorf("old name only = %d, want 5", got)
	}
	if got := (LoadedModel{AcceleratorMemory: 7, SizeVRAM: 5}).AcceleratorBytes(); got != 7 {
		t.Errorf("both = %d, want the new field, 7", got)
	}
}

func TestMirrorDeprecatedFieldsWritesTheOldNames(t *testing.T) {
	c := &DiscoveredCapacity{
		AcceleratorMemoryTotalMiB: 200, AcceleratorMemoryUsedMiB: 20, AcceleratorModel: newName,
		LoadedModels: []LoadedModel{{Name: "m", AcceleratorMemory: 7}},
	}
	c.MirrorDeprecatedFields()
	if c.VRAMTotalMiB != 200 || c.VRAMUsedMiB != 20 || c.GPUModel != "new" || c.LoadedModels[0].SizeVRAM != 7 {
		t.Errorf("the old names follow the new: %+v", c)
	}
}
