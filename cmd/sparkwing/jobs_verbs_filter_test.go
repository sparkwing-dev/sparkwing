package main

import "github.com/sparkwing-dev/sparkwing/pkg/store"

var _ func(
	[]*store.Run,
	string,
	string,
	int,
	func(string) (map[string]string, error),
) []*store.Run = narrowRunsByRepo
