//go:build !unix

package policy

func copyOwner(path, from string) {}
