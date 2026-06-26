package main

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/boxslot"
)

func TestApplyBoxSlotControl(t *testing.T) {
	dir := t.TempDir()

	if err := applyBoxSlotControl(dir, "4"); err != nil {
		t.Fatalf("set 4: %v", err)
	}
	if v, _, _ := boxslot.ReadControl(dir); v != "4" {
		t.Fatalf("control = %q, want 4", v)
	}

	if err := applyBoxSlotControl(dir, "OFF"); err != nil {
		t.Fatalf("set OFF: %v", err)
	}
	if v, _, _ := boxslot.ReadControl(dir); v != "off" {
		t.Fatalf("control = %q, want off", v)
	}

	if err := applyBoxSlotControl(dir, "0"); err != nil {
		t.Fatalf("set 0: %v", err)
	}
	if v, _, _ := boxslot.ReadControl(dir); v != "off" {
		t.Fatalf("control after 0 = %q, want off (0 disables)", v)
	}

	if err := applyBoxSlotControl(dir, "default"); err != nil {
		t.Fatalf("set default: %v", err)
	}
	if _, ok, _ := boxslot.ReadControl(dir); ok {
		t.Fatal("control still set after 'default'; want cleared")
	}

	if err := applyBoxSlotControl(dir, "lots"); err == nil {
		t.Fatal("expected an error for a non-numeric, non-keyword value")
	}
}

func TestRenderBoxSlotReport_Disabled(t *testing.T) {
	r := boxSlotReport{Cap: 0, Disabled: true, Source: "control"}
	if err := renderBoxSlotReport(r, "json"); err != nil {
		t.Fatalf("render json: %v", err)
	}
	if err := renderBoxSlotReport(r, "plain"); err != nil {
		t.Fatalf("render plain: %v", err)
	}
	if err := renderBoxSlotReport(r, "pretty"); err != nil {
		t.Fatalf("render pretty: %v", err)
	}
}
