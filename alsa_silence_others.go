//go:build !linux

package main

// silenceAlsa is a no-op on non-Linux platforms.
func silenceAlsa() {}


