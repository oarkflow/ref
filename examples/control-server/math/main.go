// Package math is deliberately not buildable as a Go binary (no func
// main): it exists only as source for `aegis compile`, which parses these
// function declarations directly and does not care what package they are
// declared in. A real `package main` here would make `go build ./...` from
// the repository root try to link it as an executable and fail for lacking
// a main function.
package math

func Add(a, b int64) int64 {
	return a + b
}

func Multiply(a, b int64) int64 {
	return a * b
}
