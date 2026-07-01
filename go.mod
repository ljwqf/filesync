module github.com/ljwqf/filesync

// go 1.23.10 fixes GO-2025-3750 (Windows O_CREATE|O_EXCL inconsistency).
// GO-2026-4602 (FileInfo escape from os.Root) requires go1.25.8 to fully fix,
// but this codebase does not use os.Root/os.OpenRoot, so it is not exploitable here.
go 1.23.10

require (
	github.com/bmatcuk/doublestar/v4 v4.6.1
	github.com/zeebo/xxh3 v1.1.0
	go.etcd.io/bbolt v1.3.11
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/klauspost/cpuid/v2 v2.2.10 // indirect
	golang.org/x/sync v0.8.0 // indirect
	golang.org/x/sys v0.30.0 // indirect
)
