package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestWindowsReplacementPreservesRunningImageBeforeInstall(t *testing.T) {
	t.Parallel()

	type call struct {
		source string
		target string
		flags  uint32
	}
	var calls []call
	move := func(source, target string, flags uint32) error {
		calls = append(calls, call{source: source, target: target, flags: flags})
		return nil
	}
	if err := replaceWindowsRunningImageWith("stage", "sparkwing.exe", move, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	want := []call{
		{source: "sparkwing.exe", target: "sparkwing.exe.old", flags: windowsMoveWriteThrough},
		{source: "stage", target: "sparkwing.exe", flags: windowsMoveWriteThrough},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("move calls = %#v, want %#v", calls, want)
	}
}

func TestWindowsReplacementRestoresPreservedImageWhenInstallFails(t *testing.T) {
	t.Parallel()

	var calls [][2]string
	move := func(source, target string, _ uint32) error {
		calls = append(calls, [2]string{source, target})
		if source == "stage" {
			return errors.New("sharing violation")
		}
		return nil
	}
	if err := replaceWindowsRunningImageWith("stage", "sparkwing.exe", move, func(string) error { return nil }); err == nil {
		t.Fatal("replaceWindowsRunningImageWith() succeeded")
	}
	want := [][2]string{
		{"sparkwing.exe", "sparkwing.exe.old"},
		{"stage", "sparkwing.exe"},
		{"sparkwing.exe.old", "sparkwing.exe"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("move calls = %#v, want %#v", calls, want)
	}
}

func TestWindowsRollbackRestoresRunningImageAfterInstalledVerificationFails(t *testing.T) {
	t.Parallel()

	var calls [][2]string
	move := func(source, target string, _ uint32) error {
		calls = append(calls, [2]string{source, target})
		return nil
	}
	remove := func(path string) error {
		if path == "sparkwing.exe.old" {
			return errors.New("running image is still mapped")
		}
		return nil
	}
	if err := restoreWindowsRunningImageWith("rollback", "sparkwing.exe", move, remove); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{
		{"sparkwing.exe", "sparkwing.exe.failed"},
		{"sparkwing.exe.old", "sparkwing.exe"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("move calls = %#v, want %#v", calls, want)
	}
}
