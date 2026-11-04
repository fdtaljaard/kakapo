package service

import (
	"errors"
	"testing"

	"sqlmonitor/internal/domain"
)

// Both index fixes are in the registry, so prepareGenericFix and ApplyFixWithOptions use them.
func TestIndexFixesRegistered(t *testing.T) {
	for _, id := range []string{domain.FixCreateIndex, domain.FixDropIndex} {
		if _, ok := registeredFixFor(id); !ok {
			t.Errorf("%s is not registered", id)
		}
		if _, ok := domain.FindFixDef(id); !ok {
			t.Errorf("%s has no definition", id)
		}
	}
}

func TestIndexFixOptions(t *testing.T) {
	var se *Error
	spec, err := createIndexSpec(map[string]string{domain.OptionTable: "[WarehouseLive].[dbo].[Pick]", domain.OptionEquality: "[Status]"}, "warehouselive")
	if err != nil || spec.Schema != "dbo" || spec.Table != "Pick" {
		t.Errorf("spec %+v %v", spec, err)
	}
	if _, err := createIndexSpec(map[string]string{domain.OptionTable: "[Other].[dbo].[Pick]", domain.OptionEquality: "[Status]"}, "WarehouseLive"); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("another database: want KindConflict, got %v", err)
	}
	if _, err := createIndexSpec(map[string]string{domain.OptionTable: "dbo.Pick"}, "WarehouseLive"); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("no keys: want KindInvalid, got %v", err)
	}

	schema, table, index, err := dropIndexTarget(map[string]string{domain.OptionIndex: " IX_A ", domain.OptionTable: "[dbo].[Pick]"}, "WarehouseLive")
	if err != nil || schema != "dbo" || table != "Pick" || index != "IX_A" {
		t.Errorf("drop target %s %s %s %v", schema, table, index, err)
	}
	if _, _, _, err := dropIndexTarget(map[string]string{domain.OptionTable: "dbo.Pick"}, "WarehouseLive"); !errors.As(err, &se) || se.Kind != KindInvalid {
		t.Errorf("no index: want KindInvalid, got %v", err)
	}
	if _, _, _, err := dropIndexTarget(map[string]string{domain.OptionIndex: "IX_A", domain.OptionTable: "X.dbo.Pick"}, "WarehouseLive"); !errors.As(err, &se) || se.Kind != KindConflict {
		t.Errorf("another database: want KindConflict, got %v", err)
	}
}
