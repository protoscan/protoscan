# protoscan

[![Build Status](https://cloud.drone.io/api/badges/danil/protoscan/status.svg)](https://cloud.drone.io/danil/protoscan)
[![Go Reference](https://pkg.go.dev/badge/github.com/danil/protoscan.svg)](https://pkg.go.dev/github.com/danil/protoscan)

Protocol scanner for Go.  
Source files are distributed under the BSD-style license
found in the [LICENSE](./LICENSE) file.

## Install

    go get github.com/danil/protoscan@v0.0.44

## Benchmark

```
go test -bench=. ./...
goarch: amd64
pkg: github.com/danil/protoscan
cpu: 11th Gen Intel(R) Core(TM) i5-1135G7 @ 2.40GHz
BenchmarkScanRune-8   	 1464958	       812.0 ns/op	      40 B/op	       2 allocs/op
```
