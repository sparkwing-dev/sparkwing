package main

import (
	"errors"
	"reflect"
	"testing"
)

var (
	_ func(string, string, func(string, string, uint32) error) error = replaceWindowsRunningImageWith
	_ func(string, string, func(string, string, uint32) error) error = restoreWindowsRunningImageWith
)

func TestWindowsReplacementUsesOneAtomicOperation(t *testing.T) {
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
	if err := replaceWindowsRunningImageWith("stage", "sparkwing.exe", move); err != nil {
		t.Fatal(err)
	}
	want := []call{{source: "stage", target: "sparkwing.exe", flags: windowsMoveReplaceExisting | windowsMoveWriteThrough}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("move calls = %#v, want %#v", calls, want)
	}
}

func TestWindowsReplacementFailsWithoutRenameAsideFallback(t *testing.T) {
	t.Parallel()

	var calls [][2]string
	move := func(source, target string, _ uint32) error {
		calls = append(calls, [2]string{source, target})
		return errors.New("sharing violation")
	}
	if err := replaceWindowsRunningImageWith("stage", "sparkwing.exe", move); err == nil {
		t.Fatal("replaceWindowsRunningImageWith() succeeded")
	}
	want := [][2]string{{"stage", "sparkwing.exe"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("move calls = %#v, want %#v", calls, want)
	}
}

func TestWindowsRollbackUsesOneAtomicOperation(t *testing.T) {
	t.Parallel()

	var calls [][2]string
	move := func(source, target string, _ uint32) error {
		calls = append(calls, [2]string{source, target})
		return nil
	}
	if err := restoreWindowsRunningImageWith("rollback", "sparkwing.exe", move); err != nil {
		t.Fatal(err)
	}
	want := [][2]string{{"rollback", "sparkwing.exe"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("move calls = %#v, want %#v", calls, want)
	}
}
